package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"image"
	"math"
	"sort"
	"sync"
	"time"
	"unsafe"

	rl "github.com/gen2brain/raylib-go/raylib"
)

const (
	terrainTileGridN           = 128
	terrainTileHighResDim      = 512
	terrainTileUltraResDim     = 1024
	terrainTileExtremeResDim   = 2048
	terrainTileExtremeNearestN = 8
	terrainTileUltraNearestN   = 48
	terrainTileHighNearestN    = 128 // tiles ranked beyond this drop back to base
	terrainTileUploadsPerFrame = 3
)

// Tile quality tiers, in increasing detail:
//
//	0 base    — initial slice of the loading-time mosaic (~1024² per tile)
//	1 high    — streamed at terrainTileHighResDim (everywhere, eventually)
//	2 ultra   — streamed at terrainTileUltraResDim, top-N closest tiles
//	3 extreme — streamed at terrainTileExtremeResDim, top-M closest tiles
//
// Closest tiles get the highest tier; farther rings degrade. Targets are
// recomputed on every worker iteration from the latest camera position.
const (
	tileQualityBase    = 0
	tileQualityHigh    = 1
	tileQualityUltra   = 2
	tileQualityExtreme = 3
)

type terrainTile struct {
	gridX, gridZ int

	mesh        rl.Mesh
	meshBytes   int64
	material    rl.Material
	texture     rl.Texture2D // currently-bound texture (may equal baseTexture)
	baseTexture rl.Texture2D // initial low-res slice; kept alive for cheap downgrade
	position    rl.Vector3

	worldWest, worldEast, worldSouth, worldNorth float64
	centerLocalX, centerLocalZ                   float32

	quality       int
	maxQualityCap int // highest quality tier that has not failed to upload
}

type terrainStreamResult struct {
	tileIndex int
	quality   int
	rgba      *image.RGBA
	err       error
}

type terrainStreaming struct {
	results chan terrainStreamResult
	quit    chan struct{}
	wg      sync.WaitGroup

	mu        sync.Mutex
	requested map[int]int // tileIndex -> highest quality already requested

	camMu      sync.Mutex
	camX, camZ float32
}

func buildTerrainTiles(t *terrainData, baseMosaic *image.RGBA, source *preparedTerrainSource, gridN int) []*terrainTile {
	meshW := t.meshWidth
	meshH := t.meshHeight
	spanX := float64(t.widthMeters)
	spanZ := float64(t.depthMeters)

	baseW := baseMosaic.Bounds().Dx()
	baseH := baseMosaic.Bounds().Dy()

	tiles := make([]*terrainTile, 0, gridN*gridN)
	for gz := 0; gz < gridN; gz++ {
		for gx := 0; gx < gridN; gx++ {
			// Inclusive pixel range; adjacent tiles share boundary samples
			// so vertices on shared edges line up exactly (no cracks).
			x0 := gx * (meshW - 1) / gridN
			x1 := (gx + 1) * (meshW - 1) / gridN
			z0 := gz * (meshH - 1) / gridN
			z1 := (gz + 1) * (meshH - 1) / gridN
			if gx == gridN-1 {
				x1 = meshW - 1
			}
			if gz == gridN-1 {
				z1 = meshH - 1
			}
			tileW := x1 - x0 + 1
			tileH := z1 - z0 + 1
			if tileW < 2 || tileH < 2 {
				continue
			}

			tileSpanX := float32(float64(x1-x0) / float64(meshW-1) * spanX)
			tileSpanZ := float32(float64(z1-z0) / float64(meshH-1) * spanZ)
			mesh, meshBytes := buildTerrainTileMesh(t, x0, x1, z0, z1, tileSpanX, tileSpanZ)

			worldWest := source.worldWest + float64(x0)/float64(meshW-1)*(source.worldEast-source.worldWest)
			worldEast := source.worldWest + float64(x1)/float64(meshW-1)*(source.worldEast-source.worldWest)
			worldNorth := source.worldNorth - float64(z0)/float64(meshH-1)*(source.worldNorth-source.worldSouth)
			worldSouth := source.worldNorth - float64(z1)/float64(meshH-1)*(source.worldNorth-source.worldSouth)

			bx0 := int(math.Round(float64(x0) / float64(meshW-1) * float64(baseW)))
			bx1 := int(math.Round(float64(x1) / float64(meshW-1) * float64(baseW)))
			bz0 := int(math.Round(float64(z0) / float64(meshH-1) * float64(baseH)))
			bz1 := int(math.Round(float64(z1) / float64(meshH-1) * float64(baseH)))
			if bx1 <= bx0 {
				bx1 = bx0 + 1
			}
			if bz1 <= bz0 {
				bz1 = bz0 + 1
			}
			if bx1 > baseW {
				bx1 = baseW
			}
			if bz1 > baseH {
				bz1 = baseH
			}
			sub := baseMosaic.SubImage(image.Rect(bx0, bz0, bx1, bz1))
			tileImg := goImageToRaylibImage(sub)
			tex := rl.LoadTextureFromImage(tileImg)
			rl.GenTextureMipmaps(&tex)
			rl.SetTextureFilter(tex, rl.FilterAnisotropic16x)
			rl.SetTextureWrap(tex, rl.WrapClamp)

			mat := rl.LoadMaterialDefault()
			rl.SetMaterialTexture(&mat, rl.MapAlbedo, tex)

			posX := t.position.X + float32(float64(x0)/float64(meshW-1)*spanX)
			posZ := t.position.Z + float32(float64(z0)/float64(meshH-1)*spanZ)
			posY := t.position.Y

			tiles = append(tiles, &terrainTile{
				gridX:         gx,
				gridZ:         gz,
				mesh:          mesh,
				meshBytes:     meshBytes,
				material:      mat,
				texture:       tex,
				baseTexture:   tex,
				position:      rl.NewVector3(posX, posY, posZ),
				worldWest:     worldWest,
				worldEast:     worldEast,
				worldSouth:    worldSouth,
				worldNorth:    worldNorth,
				centerLocalX:  posX + tileSpanX*0.5,
				centerLocalZ:  posZ + tileSpanZ*0.5,
				maxQualityCap: tileQualityExtreme,
			})
		}
	}
	return tiles
}

func terrainMeshIndexSentinel() *uint16 {
	return (*uint16)(C.malloc(C.size_t(2)))
}

func freeTerrainMeshIndexSentinel(ptr *uint16) {
	if ptr != nil {
		C.free(unsafe.Pointer(ptr))
	}
}

func buildTerrainTileMesh(t *terrainData, x0, x1, z0, z1 int, tileSpanX, tileSpanZ float32) (rl.Mesh, int64) {
	tileW := x1 - x0 + 1
	tileH := z1 - z0 + 1
	vertexCount := tileW * tileH
	triangleCount := (tileW - 1) * (tileH - 1) * 2

	vertices := make([]float32, vertexCount*3)
	normals := make([]float32, vertexCount*3)
	texcoords := make([]float32, vertexCount*2)
	indices := make([]uint16, triangleCount*3)

	stepX := tileSpanX / float32(tileW-1)
	stepZ := tileSpanZ / float32(tileH-1)
	fullStepX := float64(t.widthMeters) / float64(t.meshWidth-1)
	fullStepZ := float64(t.depthMeters) / float64(t.meshHeight-1)

	for z := 0; z < tileH; z++ {
		srcZ := z0 + z
		for x := 0; x < tileW; x++ {
			srcX := x0 + x
			i := z*tileW + x
			v := i * 3
			tc := i * 2

			vertices[v] = float32(x) * stepX
			vertices[v+1] = float32(t.heightSamples[srcZ*t.meshWidth+srcX] - t.heightMin)
			vertices[v+2] = float32(z) * stepZ
			texcoords[tc] = float32(x) / float32(tileW-1)
			texcoords[tc+1] = float32(z) / float32(tileH-1)

			leftX := max(srcX-1, 0)
			rightX := min(srcX+1, t.meshWidth-1)
			upZ := max(srcZ-1, 0)
			downZ := min(srcZ+1, t.meshHeight-1)
			dx := float64(rightX-leftX) * fullStepX
			dz := float64(downZ-upZ) * fullStepZ
			if dx <= 0 {
				dx = 1
			}
			if dz <= 0 {
				dz = 1
			}
			dhDx := (t.heightSamples[srcZ*t.meshWidth+rightX] - t.heightSamples[srcZ*t.meshWidth+leftX]) / dx
			dhDz := (t.heightSamples[downZ*t.meshWidth+srcX] - t.heightSamples[upZ*t.meshWidth+srcX]) / dz
			nx, ny, nz := -dhDx, 1.0, -dhDz
			invLen := 1.0 / math.Sqrt(nx*nx+ny*ny+nz*nz)
			normals[v] = float32(nx * invLen)
			normals[v+1] = float32(ny * invLen)
			normals[v+2] = float32(nz * invLen)
		}
	}

	out := 0
	for z := 0; z < tileH-1; z++ {
		for x := 0; x < tileW-1; x++ {
			topLeft := uint16(z*tileW + x)
			bottomLeft := uint16((z+1)*tileW + x)
			topRight := uint16(z*tileW + x + 1)
			bottomRight := uint16((z+1)*tileW + x + 1)
			indices[out] = topLeft
			indices[out+1] = bottomLeft
			indices[out+2] = topRight
			indices[out+3] = topRight
			indices[out+4] = bottomLeft
			indices[out+5] = bottomRight
			out += 6
		}
	}

	mesh := rl.Mesh{
		VertexCount:   int32(vertexCount),
		TriangleCount: int32(triangleCount),
		Vertices:      &vertices[0],
		Normals:       &normals[0],
		Texcoords:     &texcoords[0],
		Indices:       &indices[0],
	}
	rl.UploadMesh(&mesh, false)

	meshBytes := int64(vertexCount)*((3+3+2)*4) + int64(triangleCount)*3*2

	// UploadMesh copies data to GPU buffers. Keep a C-allocated non-nil index
	// sentinel because Raylib uses mesh.Indices as the indexed-draw flag, but
	// do not keep Go slice pointers in the stored Mesh.
	mesh.Vertices = nil
	mesh.Normals = nil
	mesh.Texcoords = nil
	mesh.Indices = terrainMeshIndexSentinel()
	return mesh, meshBytes
}

func drawTerrainTiles(t *terrainData) {
	for _, tile := range t.tiles {
		rl.DrawMesh(tile.mesh, tile.material, rl.MatrixTranslate(tile.position.X, tile.position.Y, tile.position.Z))
	}
}

func unloadTerrainTiles(t *terrainData) {
	if t == nil {
		return
	}
	if t.streaming != nil {
		close(t.streaming.quit)
		t.streaming.wg.Wait()
		// Drain any in-flight results so their image buffers can be GC'd.
		for {
			select {
			case <-t.streaming.results:
				continue
			default:
			}
			break
		}
		t.streaming = nil
	}
	for _, tile := range t.tiles {
		freeTerrainMeshIndexSentinel(tile.mesh.Indices)
		tile.mesh.Indices = nil
		rl.UnloadMesh(&tile.mesh)
		// UnloadMaterial unloads the currently-bound albedo texture. If the
		// base texture isn't the one bound (i.e. tile is upgraded), free it
		// explicitly so it doesn't leak.
		if tile.baseTexture.ID != 0 && tile.baseTexture.ID != tile.texture.ID {
			rl.UnloadTexture(tile.baseTexture)
		}
		rl.UnloadMaterial(tile.material)
	}
	t.tiles = nil
}

func startTerrainStreaming(t *terrainData) {
	if t == nil || len(t.tiles) == 0 {
		return
	}
	s := &terrainStreaming{
		results:   make(chan terrainStreamResult, terrainTileUploadsPerFrame),
		quit:      make(chan struct{}),
		requested: make(map[int]int),
	}
	t.streaming = s
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		idleTimer := time.NewTimer(0)
		if !idleTimer.Stop() {
			<-idleTimer.C
		}
		for {
			select {
			case <-s.quit:
				return
			default:
			}

			nextIdx, nextQuality := pickNextStreamingJob(t, s)
			if nextIdx < 0 {
				// Nothing to do right now; idle and re-check after a beat so we
				// pick up tiles whose target quality rises as the camera moves.
				idleTimer.Reset(150 * time.Millisecond)
				select {
				case <-s.quit:
					if !idleTimer.Stop() {
						<-idleTimer.C
					}
					return
				case <-idleTimer.C:
				}
				continue
			}

			tile := t.tiles[nextIdx]
			dim := terrainTileHighResDim
			switch nextQuality {
			case tileQualityUltra:
				dim = terrainTileUltraResDim
			case tileQualityExtreme:
				dim = terrainTileExtremeResDim
			}
			rgba, _, _, err := buildOrthoMosaic(t.orthoTiles, tile.worldWest, tile.worldEast, tile.worldSouth, tile.worldNorth, dim)
			select {
			case <-s.quit:
				return
			case s.results <- terrainStreamResult{tileIndex: nextIdx, quality: nextQuality, rgba: rgba, err: err}:
			}
		}
	}()
}

type tileDist struct {
	idx int
	d2  float32
}

// computeTileTargets returns each tile's target quality tier and the tiles
// sorted nearest-first. The closest N rings get progressively higher tiers;
// tiles beyond the high-tier ring drop back to base so VRAM stays bounded.
func computeTileTargets(t *terrainData, camX, camZ float32) ([]int, []tileDist) {
	dists := make([]tileDist, len(t.tiles))
	for i, tile := range t.tiles {
		dx := tile.centerLocalX - camX
		dz := tile.centerLocalZ - camZ
		dists[i] = tileDist{i, dx*dx + dz*dz}
	}
	sort.Slice(dists, func(a, b int) bool { return dists[a].d2 < dists[b].d2 })

	target := make([]int, len(t.tiles))
	for rank, d := range dists {
		switch {
		case rank < terrainTileExtremeNearestN:
			target[d.idx] = tileQualityExtreme
		case rank < terrainTileUltraNearestN:
			target[d.idx] = tileQualityUltra
		case rank < terrainTileHighNearestN:
			target[d.idx] = tileQualityHigh
		default:
			target[d.idx] = tileQualityBase
		}
		if cap := t.tiles[d.idx].maxQualityCap; target[d.idx] > cap {
			target[d.idx] = cap
		}
	}
	return target, dists
}

// pickNextStreamingJob picks the best upgrade job: the tile with the largest
// quality deficit (target − current), ties broken by camera distance.
func pickNextStreamingJob(t *terrainData, s *terrainStreaming) (int, int) {
	s.camMu.Lock()
	cx, cz := s.camX, s.camZ
	s.camMu.Unlock()

	target, dists := computeTileTargets(t, cx, cz)

	s.mu.Lock()
	defer s.mu.Unlock()
	bestIdx := -1
	bestQuality := tileQualityBase
	bestDeficit := 0
	var bestD float32 = math.MaxFloat32
	for _, d := range dists {
		i := d.idx
		tgt := target[i]
		have := t.tiles[i].quality
		if reqQ, ok := s.requested[i]; ok && reqQ >= tgt {
			continue
		}
		if have >= tgt {
			continue
		}
		deficit := tgt - have
		if deficit > bestDeficit || (deficit == bestDeficit && d.d2 < bestD) {
			bestDeficit = deficit
			bestIdx = i
			bestQuality = tgt
			bestD = d.d2
		}
	}
	if bestIdx >= 0 {
		s.requested[bestIdx] = bestQuality
	}
	return bestIdx, bestQuality
}

// pumpTerrainStreaming runs one frame of the tile streaming logic:
//  1. Reports the latest camera XZ to the worker for prioritization.
//  2. Downgrades tiles that have drifted out of their target ring back to the
//     base texture so VRAM stays bounded as the camera moves around.
//  3. Uploads up to terrainTileUploadsPerFrame ready high-res results.
func pumpTerrainStreaming(t *terrainData, cameraX, cameraZ float32) {
	if t == nil || t.streaming == nil {
		return
	}
	s := t.streaming
	s.camMu.Lock()
	s.camX = cameraX
	s.camZ = cameraZ
	s.camMu.Unlock()

	target, dists := computeTileTargets(t, cameraX, cameraZ)

	// Downgrade pass: walk far→near and drop tiles whose current quality
	// exceeds their target. Going furthest-first ensures the highest-VRAM
	// outliers are reclaimed first.
	downgrades := 0
	for i := len(dists) - 1; i >= 0 && downgrades < terrainTileUploadsPerFrame; i-- {
		idx := dists[i].idx
		tile := t.tiles[idx]
		if target[idx] >= tile.quality {
			continue
		}
		// Drop straight back to base — we always keep tile.baseTexture alive,
		// so this is a constant-time rebind with no GPU upload. Subsequent
		// camera approaches will re-stream the upper tiers normally.
		if tile.texture.ID != tile.baseTexture.ID {
			oldTex := tile.texture
			rl.SetMaterialTexture(&tile.material, rl.MapAlbedo, tile.baseTexture)
			rl.UnloadTexture(oldTex)
			tile.texture = tile.baseTexture
		}
		tile.quality = tileQualityBase
		// Allow the worker to pick this tile up again as the camera moves.
		s.mu.Lock()
		delete(s.requested, idx)
		s.mu.Unlock()
		downgrades++
	}

	for budget := 0; budget < terrainTileUploadsPerFrame; budget++ {
		select {
		case res := <-s.results:
			if res.err != nil || res.rgba == nil || res.tileIndex < 0 || res.tileIndex >= len(t.tiles) {
				continue
			}
			tile := t.tiles[res.tileIndex]
			if tile.quality >= res.quality {
				// Already at or above this quality (e.g., a stale result for a
				// lower tier finished after the tile was upgraded).
				continue
			}
			// Always apply the upload even if the tile's target tier dropped
			// while the worker was processing. If it really has fallen out
			// of its ring, the next frame's downgrade pass will reclaim the
			// VRAM. Skipping uploads here was preventing tiles from ever
			// upgrading whenever the camera moved during a long gdal job.
			rlImg := goImageToRaylibImage(res.rgba)
			newTex := rl.LoadTextureFromImage(rlImg)
			if newTex.ID == 0 {
				// Upload failed (e.g., the texture exceeds the GPU's max size).
				// Lower the cap so the worker retries this tile at a tier the
				// driver actually accepts, and forget the failed-tier marker so
				// pickNextStreamingJob can pick it up again.
				if res.quality > tileQualityBase {
					tile.maxQualityCap = res.quality - 1
				}
				s.mu.Lock()
				delete(s.requested, res.tileIndex)
				s.mu.Unlock()
				continue
			}
			rl.GenTextureMipmaps(&newTex)
			rl.SetTextureFilter(newTex, rl.FilterAnisotropic16x)
			rl.SetTextureWrap(newTex, rl.WrapClamp)

			oldTex := tile.texture
			rl.SetMaterialTexture(&tile.material, rl.MapAlbedo, newTex)
			// Never unload the always-alive base texture — we still need it
			// for cheap downgrade rebinds.
			if oldTex.ID != tile.baseTexture.ID {
				rl.UnloadTexture(oldTex)
			}
			tile.texture = newTex
			tile.quality = res.quality
		default:
			return
		}
	}
}
