package main

import (
	"math"
	"runtime"
	"sort"
	"sync"
	"time"
)

// Streaming radii are in world (meter) units. Load < Evict adds hysteresis so
// regions on the boundary don't flap as the camera jitters.
const (
	buildingLoadRadius          = 1800.0
	buildingEvictRadius         = 2400.0
	buildingMaxResident         = 16
	buildingUploadStepsPerFrame = 4
)

type buildingRegionState int

const (
	regionStateUnloaded buildingRegionState = iota
	regionStateRequested
	regionStateParsed
	regionStateUploading
	regionStateLoaded
)

type buildingParseResult struct {
	regionIdx int
	data      parsedBuildingGLB
	err       error
}

// buildingStreaming coordinates async parsing and incremental main-thread
// GPU upload of building region GLBs based on camera distance.
//
// Parsing happens on worker goroutines (CPU only — no rl.* calls). The main
// thread drains parse results in pumpBuildingStreaming and feeds them into
// the existing buildingRegionUpload pipeline, advancing a few mesh/texture
// uploads per frame so any hitch is bounded.
type buildingStreaming struct {
	results chan buildingParseResult
	quit    chan struct{}
	wg      sync.WaitGroup

	mu      sync.Mutex
	parsed  map[int]parsedBuildingGLB
	uploads map[int]*buildingRegionUpload

	camMu      sync.Mutex
	camX, camZ float32
}

func startBuildingStreaming(objects *sceneObjects, camX, camZ float32) {
	if objects == nil || len(objects.BuildingRegions) == 0 || objects.streaming != nil {
		return
	}
	workers := runtime.NumCPU() / 2
	if workers < 1 {
		workers = 1
	}
	if workers > 3 {
		workers = 3
	}
	s := &buildingStreaming{
		results: make(chan buildingParseResult, workers),
		quit:    make(chan struct{}),
		parsed:  map[int]parsedBuildingGLB{},
		uploads: map[int]*buildingRegionUpload{},
	}
	s.camX = camX
	s.camZ = camZ
	objects.streaming = s
	for w := 0; w < workers; w++ {
		s.wg.Add(1)
		go buildingStreamingWorker(objects, s)
	}
}

func stopBuildingStreaming(objects *sceneObjects) {
	if objects == nil || objects.streaming == nil {
		return
	}
	close(objects.streaming.quit)
	objects.streaming.wg.Wait()
	for {
		select {
		case <-objects.streaming.results:
		default:
			objects.streaming = nil
			return
		}
	}
}

func buildingStreamingWorker(objects *sceneObjects, s *buildingStreaming) {
	defer s.wg.Done()
	idle := time.NewTimer(0)
	if !idle.Stop() {
		<-idle.C
	}
	for {
		select {
		case <-s.quit:
			return
		default:
		}

		idx := pickNextBuildingParseJob(objects, s)
		if idx < 0 {
			idle.Reset(150 * time.Millisecond)
			select {
			case <-s.quit:
				if !idle.Stop() {
					<-idle.C
				}
				return
			case <-idle.C:
			}
			continue
		}

		path := objects.BuildingRegions[idx].Path
		data, _, err := parseBuildingGLBWithMetadata(path)
		select {
		case <-s.quit:
			return
		case s.results <- buildingParseResult{regionIdx: idx, data: data, err: err}:
		}
	}
}

func pickNextBuildingParseJob(objects *sceneObjects, s *buildingStreaming) int {
	s.camMu.Lock()
	cx, cz := s.camX, s.camZ
	s.camMu.Unlock()

	loadR2 := float32(buildingLoadRadius * buildingLoadRadius)

	s.mu.Lock()
	defer s.mu.Unlock()

	resident := 0
	for i := range objects.BuildingRegions {
		if objects.BuildingRegions[i].State != regionStateUnloaded {
			resident++
		}
	}
	if resident >= buildingMaxResident {
		return -1
	}

	bestIdx := -1
	bestD2 := float32(math.MaxFloat32)
	for i := range objects.BuildingRegions {
		region := &objects.BuildingRegions[i]
		if region.State != regionStateUnloaded {
			continue
		}
		d2 := regionDistSquared(region, cx, cz)
		if d2 > loadR2 {
			continue
		}
		if d2 < bestD2 {
			bestD2 = d2
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		return -1
	}
	objects.BuildingRegions[bestIdx].State = regionStateRequested
	return bestIdx
}

// pumpBuildingStreaming runs one frame of streaming on the main thread:
//  1. Updates the worker-visible camera position.
//  2. Evicts regions that drifted past the evict radius.
//  3. Pressure-evicts the farthest loaded region if the cap is full but a
//     closer unloaded region needs a slot.
//  4. Drains any newly parsed results into the upload pool.
//  5. Advances up to buildingUploadStepsPerFrame upload steps.
func pumpBuildingStreaming(objects *sceneObjects, camX, camZ float32) {
	if objects == nil || objects.streaming == nil {
		return
	}
	s := objects.streaming
	s.camMu.Lock()
	s.camX = camX
	s.camZ = camZ
	s.camMu.Unlock()

	evictR2 := float32(buildingEvictRadius * buildingEvictRadius)
	loadR2 := float32(buildingLoadRadius * buildingLoadRadius)

	s.mu.Lock()

	// Regular eviction: remove regions that drifted past the evict radius.
	for i := range objects.BuildingRegions {
		region := &objects.BuildingRegions[i]
		if regionDistSquared(region, camX, camZ) <= evictR2 {
			continue
		}
		switch region.State {
		case regionStateLoaded:
			unloadStreamedBuildingModel(region.Model)
			region.Model = streamedBuildingModel{}
			region.State = regionStateUnloaded
		case regionStateParsed:
			delete(s.parsed, i)
			region.State = regionStateUnloaded
		case regionStateUploading:
			if up, ok := s.uploads[i]; ok {
				unloadStreamedBuildingModel(up.Model)
				delete(s.uploads, i)
			}
			region.State = regionStateUnloaded
		}
		// Requested: let the worker finish; the result drain will discard it
		// if the region is still beyond evict range.
	}

	// Pressure eviction: the hysteresis band [loadRadius, evictRadius] can fill
	// all resident slots with regions the camera has already moved away from,
	// blocking closer regions from ever loading. When the cap is full, find the
	// nearest unloaded region within loadRadius; if any loaded region is farther
	// away, evict the farthest one to free a slot.
	resident := 0
	for i := range objects.BuildingRegions {
		if objects.BuildingRegions[i].State != regionStateUnloaded {
			resident++
		}
	}
	if resident >= buildingMaxResident {
		nearestUnloadedD2 := float32(math.MaxFloat32)
		for i := range objects.BuildingRegions {
			if objects.BuildingRegions[i].State != regionStateUnloaded {
				continue
			}
			d2 := regionDistSquared(&objects.BuildingRegions[i], camX, camZ)
			if d2 <= loadR2 && d2 < nearestUnloadedD2 {
				nearestUnloadedD2 = d2
			}
		}
		if nearestUnloadedD2 < math.MaxFloat32 {
			farthestIdx := -1
			farthestD2 := nearestUnloadedD2 // only evict if strictly farther
			for i := range objects.BuildingRegions {
				region := &objects.BuildingRegions[i]
				if region.State != regionStateLoaded {
					continue
				}
				d2 := regionDistSquared(region, camX, camZ)
				if d2 > farthestD2 {
					farthestD2 = d2
					farthestIdx = i
				}
			}
			if farthestIdx >= 0 {
				region := &objects.BuildingRegions[farthestIdx]
				unloadStreamedBuildingModel(region.Model)
				region.Model = streamedBuildingModel{}
				region.State = regionStateUnloaded
			}
		}
	}

	s.mu.Unlock()

	// Drain parse results.
drain:
	for {
		select {
		case res := <-s.results:
			s.mu.Lock()
			region := &objects.BuildingRegions[res.regionIdx]
			if res.err != nil {
				region.State = regionStateUnloaded
				s.mu.Unlock()
				continue
			}
			if regionDistSquared(region, camX, camZ) > evictR2 {
				region.State = regionStateUnloaded
				s.mu.Unlock()
				continue
			}
			s.parsed[res.regionIdx] = res.data
			region.State = regionStateParsed
			s.mu.Unlock()
		default:
			break drain
		}
	}

	// Time-budgeted upload pump. Each step uploads one mesh or one texture.
	for step := 0; step < buildingUploadStepsPerFrame; step++ {
		idx, upload := pickNextUpload(objects, s)
		if upload == nil {
			break
		}
		done, err := advanceBuildingRegionUpload(upload)
		if err != nil {
			unloadStreamedBuildingModel(upload.Model)
			s.mu.Lock()
			delete(s.uploads, idx)
			objects.BuildingRegions[idx].State = regionStateUnloaded
			s.mu.Unlock()
			continue
		}
		if done {
			s.mu.Lock()
			objects.BuildingRegions[idx].Model = upload.Model
			objects.BuildingRegions[idx].State = regionStateLoaded
			delete(s.uploads, idx)
			s.mu.Unlock()
		}
	}
}

// pickNextUpload picks the upload to advance: an in-progress upload (nearest
// first), else starts a new upload from the nearest parsed region. Holds the
// streaming mutex only while picking, not while uploading.
func pickNextUpload(objects *sceneObjects, s *buildingStreaming) (int, *buildingRegionUpload) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cx, cz := s.camX, s.camZ

	bestIdx := -1
	bestD2 := float32(math.MaxFloat32)
	for idx := range s.uploads {
		region := &objects.BuildingRegions[idx]
		d2 := regionDistSquared(region, cx, cz)
		if d2 < bestD2 {
			bestD2 = d2
			bestIdx = idx
		}
	}
	if bestIdx >= 0 {
		return bestIdx, s.uploads[bestIdx]
	}

	type cand struct {
		idx int
		d2  float32
	}
	cands := make([]cand, 0, len(s.parsed))
	for idx := range s.parsed {
		region := &objects.BuildingRegions[idx]
		cands = append(cands, cand{idx, regionDistSquared(region, cx, cz)})
	}
	if len(cands) == 0 {
		return -1, nil
	}
	sort.Slice(cands, func(a, b int) bool { return cands[a].d2 < cands[b].d2 })
	idx := cands[0].idx
	data := s.parsed[idx]
	delete(s.parsed, idx)
	upload := newBuildingRegionUpload(data)
	s.uploads[idx] = upload
	objects.BuildingRegions[idx].State = regionStateUploading
	return idx, upload
}

// residentBuildingRegions returns counts of regions in each non-unloaded state
// for HUD/diagnostics.
func residentBuildingRegions(objects *sceneObjects) (loaded, inFlight int) {
	if objects == nil {
		return 0, 0
	}
	for i := range objects.BuildingRegions {
		switch objects.BuildingRegions[i].State {
		case regionStateLoaded:
			loaded++
		case regionStateRequested, regionStateParsed, regionStateUploading:
			inFlight++
		}
	}
	return
}
