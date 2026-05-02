package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRoadMaskPolygonsGeoTransform(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roads.json")
	data := []byte(`{
		"geotiff": {
			"geo_transform": [100, 0.5, 0, 200, 0, -0.5]
		},
		"nodes": [
			{"id": 1, "x": 0, "y": 0},
			{"id": 2, "x": 20, "y": 0},
			{"id": 3, "x": 20, "y": 20},
			{"id": 4, "x": 0, "y": 20}
		],
		"edges": [{
			"id": 7,
			"node_ids": [1, 2, 3, 4, 1],
			"segs": [
				{"is_spline": false, "curb": "hard"},
				{"is_spline": false, "curb": "soft"},
				{"is_spline": false, "curb": "hard"},
				{"is_spline": false, "curb": "soft"}
			]
		}]
	}`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	terrain := &terrainData{centerWorldX: 100, centerWorldY: 200}
	polys, err := loadRoadMaskPolygons(path, terrain)
	if err != nil {
		t.Fatal(err)
	}
	if len(polys) != 1 {
		t.Fatalf("got %d polygons, want 1", len(polys))
	}
	got := polys[0].Points
	want := []roadPoint{{0, 0}, {10, 0}, {10, 10}, {0, 10}}
	if len(got) != len(want) {
		t.Fatalf("got %d points, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("point %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestRoadHeightLayerHardAndSoftCurbs(t *testing.T) {
	layer := &roadSurfaceLayer{
		HeightPolygons: []roadHeightPolygon{{
			Points: []roadPoint{{0, 0}, {10, 0}, {10, 10}, {0, 10}},
			Segments: []roadSegment{
				{A: roadPoint{0, 0}, B: roadPoint{10, 0}, Curb: roadCurbHard},
				{A: roadPoint{10, 0}, B: roadPoint{10, 10}, Curb: roadCurbHard},
				{A: roadPoint{10, 10}, B: roadPoint{0, 10}, Curb: roadCurbHard},
				{A: roadPoint{0, 10}, B: roadPoint{0, 0}, Curb: roadCurbSoft},
			},
			Bounds: roadBounds{MinX: 0, MaxX: 10, MinZ: 0, MaxZ: 10},
			Area:   100,
		}},
	}

	base := float32(5)
	center := layer.heightAtLocal(5, 5, base)
	if math.Abs(float64(center-(base-roadSinkMeters))) > 1e-5 {
		t.Fatalf("center height = %.4f, want %.4f", center, base-roadSinkMeters)
	}

	insideSoftEdge := layer.heightAtLocal(0.25, 5, base)
	if math.Abs(float64(insideSoftEdge-(base-roadSinkMeters))) > 1e-5 {
		t.Fatalf("inside soft edge height = %.4f, want %.4f", insideSoftEdge, base-roadSinkMeters)
	}

	outsideSoftRamp := layer.heightAtLocal(-0.25, 5, base)
	wantRamp := base - roadSinkMeters*(1-0.25/roadSoftCurbWidthMeters)
	if math.Abs(float64(outsideSoftRamp-wantRamp)) > 1e-5 {
		t.Fatalf("outside soft ramp height = %.4f, want %.4f", outsideSoftRamp, wantRamp)
	}

	outsideSoftEnd := layer.heightAtLocal(-0.25, 9.95, base)
	if outsideSoftEnd <= outsideSoftRamp {
		t.Fatalf("outside soft end height = %.4f, want above middle ramp %.4f", outsideSoftEnd, outsideSoftRamp)
	}

	outside := layer.heightAtLocal(-1, 5, base)
	if outside != base {
		t.Fatalf("outside height = %.4f, want %.4f", outside, base)
	}
}

func TestSoftCurbEndCapsApplyOnlyAtContinuousRunEnds(t *testing.T) {
	segments := []roadSegment{
		{A: roadPoint{0, 0}, B: roadPoint{10, 0}, Curb: roadCurbHard},
		{A: roadPoint{10, 0}, B: roadPoint{10, 10}, Curb: roadCurbHard},
		{A: roadPoint{10, 10}, B: roadPoint{0, 10}, Curb: roadCurbHard},
	}
	for z := float32(10); z > 0; z -= roadBoundaryMaxSegmentMeters {
		nextZ := max32(z-roadBoundaryMaxSegmentMeters, 0)
		segments = append(segments, roadSegment{
			A:    roadPoint{0, z},
			B:    roadPoint{0, nextZ},
			Curb: roadCurbSoft,
		})
	}
	layer := &roadSurfaceLayer{
		HeightPolygons: []roadHeightPolygon{{
			Points:   []roadPoint{{0, 0}, {10, 0}, {10, 10}, {0, 10}},
			Segments: segments,
			Bounds:   roadBounds{MinX: 0, MaxX: 10, MinZ: 0, MaxZ: 10},
			Area:     100,
		}},
	}

	base := float32(5)
	got := layer.heightAtLocal(-0.25, 5, base)
	want := base - roadSinkMeters*(1-0.25/roadSoftCurbWidthMeters)
	if math.Abs(float64(got-want)) > 1e-5 {
		t.Fatalf("split soft curb height = %.4f, want %.4f", got, want)
	}

	gotNearRunStart := layer.heightAtLocal(-0.25, 9.5, base)
	runT := float32(0.5) / roadSoftCurbWidthMeters
	wantNearRunStart := base - roadSinkMeters*(1-0.25/roadSoftCurbWidthMeters)*roadSmoothstep(runT)
	if math.Abs(float64(gotNearRunStart-wantNearRunStart)) > 1e-5 {
		t.Fatalf("split soft curb near run start height = %.4f, want %.4f", gotNearRunStart, wantNearRunStart)
	}
}

func TestSoftEdgeFillerSpansRoadToSoftSlope(t *testing.T) {
	terrain := &terrainData{
		heightSamples: []float64{
			0, 0,
			0, 0,
		},
		widthMeters: 10,
		depthMeters: 10,
		meshWidth:   2,
		meshHeight:  2,
	}
	layout := terrainTileLayout{
		posX:       -1,
		posZ:       -1,
		tileSpanX:  12,
		tileSpanZ:  12,
		worldWest:  -1,
		worldEast:  11,
		worldNorth: 1,
		worldSouth: -11,
	}
	polygon := roadHeightPolygon{
		Segments: []roadSegment{
			{A: roadPoint{0, 0}, B: roadPoint{10, 0}, Curb: roadCurbHard},
			{A: roadPoint{10, 0}, B: roadPoint{10, 10}, Curb: roadCurbHard},
			{A: roadPoint{10, 10}, B: roadPoint{0, 10}, Curb: roadCurbHard},
			{A: roadPoint{0, 10}, B: roadPoint{0, 9.75}, Curb: roadCurbSoft},
			{A: roadPoint{0, 9.75}, B: roadPoint{0, 9.5}, Curb: roadCurbSoft},
		},
		Area: 100,
	}
	var builder roadMeshBuilder

	appendRoadSoftEdgeFillerMeshForTile(
		terrain,
		layout,
		roadBounds{MinX: -1, MaxX: 11, MinZ: -1, MaxZ: 11},
		0,
		[]roadHeightPolygon{polygon},
		3,
		&builder,
	)

	if len(builder.vertices) == 0 {
		t.Fatal("expected soft edge filler vertices")
	}
	hasRoadDepth := false
	hasSoftDepth := false
	for i := 0; i+2 < len(builder.vertices); i += 3 {
		y := builder.vertices[i+1]
		if math.Abs(float64(y+roadSinkMeters)) <= 1e-5 {
			hasRoadDepth = true
		}
		if math.Abs(float64(y)) <= 1e-5 {
			hasSoftDepth = true
		}
	}
	if !hasRoadDepth || !hasSoftDepth {
		t.Fatalf("filler should include road and soft edge heights, road=%v soft=%v", hasRoadDepth, hasSoftDepth)
	}
}

func TestTerrainBaseHeightAtLocalUsesRenderedTriangle(t *testing.T) {
	terrain := &terrainData{
		heightSamples: []float64{
			0, 10,
			20, 40,
		},
		widthMeters:  10,
		depthMeters:  10,
		meshWidth:    2,
		meshHeight:   2,
		centerWorldZ: 0,
	}

	got := terrainBaseHeightAtLocal(terrain, 7.5, 7.5)
	want := float32(27.5)
	if math.Abs(float64(got-want)) > 1e-5 {
		t.Fatalf("height = %.4f, want %.4f", got, want)
	}
}

func TestTessellateRoadMaskSegmentUsesQuadraticControlPoint(t *testing.T) {
	terrain := &terrainData{}
	gt := [6]float64{0, 1, 0, 0, 0, -1}
	a := roadMaskNode{X: 0, Y: 0}
	b := roadMaskNode{X: 10, Y: 0}
	seg := roadMaskSeg{IsSpline: true, MidX: 5, MidY: 10}

	points := tessellateRoadMaskSegment(gt, terrain, a, b, seg)
	if len(points) < 5 {
		t.Fatalf("got %d spline points, want at least 5", len(points))
	}
	mid := points[len(points)/2]
	if math.Abs(float64(mid.X-5)) > 1e-5 || math.Abs(float64(mid.Z-5)) > 1e-5 {
		t.Fatalf("spline midpoint = %+v, want approximately {X:5 Z:5}", mid)
	}
}

func TestAppendHardCurbSegmentSubdividesLongSegments(t *testing.T) {
	terrain := &terrainData{
		heightSamples: []float64{
			0, 0,
			0, 0,
		},
		widthMeters: 2,
		depthMeters: 2,
		meshWidth:   2,
		meshHeight:  2,
	}
	segment := roadSegment{A: roadPoint{0, 0}, B: roadPoint{1, 0}, Curb: roadCurbHard}
	var mesh roadMeshCPU

	appendHardCurbSegment(terrain, segment, 0, []roadHeightPolygon{{Segments: []roadSegment{segment}}}, &mesh)

	vertices := len(mesh.Vertices) / 3
	wantVertices := 4 * 6
	if vertices != wantVertices {
		t.Fatalf("got %d vertices, want %d", vertices, wantVertices)
	}
}

func TestHardCurbSkipsOtherRoadMasks(t *testing.T) {
	terrain := &terrainData{
		heightSamples: []float64{
			0, 0,
			0, 0,
		},
		widthMeters: 10,
		depthMeters: 10,
		meshWidth:   2,
		meshHeight:  2,
	}
	mainRoad := roadHeightPolygon{
		Segments: []roadSegment{
			{A: roadPoint{0, 0}, B: roadPoint{10, 0}, Curb: roadCurbHard},
		},
	}
	overlapRoad := roadHeightPolygon{
		Points: []roadPoint{{4, -1}, {6, -1}, {6, 1}, {4, 1}},
		Segments: []roadSegment{
			{A: roadPoint{4, -1}, B: roadPoint{6, -1}, Curb: roadCurbHard},
			{A: roadPoint{6, -1}, B: roadPoint{6, 1}, Curb: roadCurbHard},
			{A: roadPoint{6, 1}, B: roadPoint{4, 1}, Curb: roadCurbHard},
			{A: roadPoint{4, 1}, B: roadPoint{4, -1}, Curb: roadCurbHard},
		},
		Bounds: roadBounds{MinX: 4, MaxX: 6, MinZ: -1, MaxZ: 1},
		Area:   4,
	}
	var mesh roadMeshCPU

	appendRoadCurbMeshes(terrain, 0, []roadHeightPolygon{mainRoad, overlapRoad}, &mesh)

	for i := 0; i+2 < len(mesh.Vertices); i += 3 {
		x := mesh.Vertices[i]
		if x > 4 && x < 6 {
			t.Fatalf("hard curb vertex in overlapping road mask: x=%.3f", x)
		}
	}
}

func TestAppendRoadSurfaceMeshesBuildsSoftSlopeOutsideRoad(t *testing.T) {
	terrain := &terrainData{
		heightSamples: []float64{
			0, 0,
			0, 0,
		},
		widthMeters:  12,
		depthMeters:  12,
		meshWidth:    2,
		meshHeight:   2,
		centerWorldX: 0,
		centerWorldY: 0,
	}
	layout := terrainTileLayout{
		posX:       -1,
		posZ:       -1,
		tileSpanX:  12,
		tileSpanZ:  12,
		worldWest:  -1,
		worldEast:  11,
		worldNorth: 1,
		worldSouth: -11,
	}
	polygon := roadHeightPolygon{
		Points: []roadPoint{{0, 0}, {10, 0}, {10, 10}, {0, 10}},
		Segments: []roadSegment{
			{A: roadPoint{0, 0}, B: roadPoint{10, 0}, Curb: roadCurbHard},
			{A: roadPoint{10, 0}, B: roadPoint{10, 10}, Curb: roadCurbHard},
			{A: roadPoint{10, 10}, B: roadPoint{0, 10}, Curb: roadCurbHard},
			{A: roadPoint{0, 10}, B: roadPoint{0, 0}, Curb: roadCurbSoft},
		},
		Bounds: roadBounds{MinX: 0, MaxX: 10, MinZ: 0, MaxZ: 10},
		Area:   100,
	}
	builders := map[int]*roadMeshBuilder{}
	cuts := map[int][]float32{}

	appendRoadSurfaceMeshes(terrain, []terrainTileLayout{layout}, 0, []roadHeightPolygon{polygon}, builders, cuts)

	builder := builders[0]
	if builder == nil {
		t.Fatal("missing road surface builder")
	}
	hasOutsideSlope := false
	for i := 0; i+2 < len(builder.vertices); i += 3 {
		x := builder.vertices[i]
		y := builder.vertices[i+1]
		if x < 0 && math.Abs(float64(y)) < 1e-5 {
			hasOutsideSlope = true
			break
		}
	}
	if !hasOutsideSlope {
		t.Fatal("expected textured soft slope vertices outside the road polygon")
	}
	if len(cuts[0]) <= 4*4 {
		t.Fatalf("got %d cut floats, want road polygon plus soft slope strip", len(cuts[0]))
	}
}

func TestSoftBandSkipsOtherRoadMasks(t *testing.T) {
	terrain := &terrainData{
		heightSamples: []float64{
			0, 0,
			0, 0,
		},
		widthMeters:  12,
		depthMeters:  12,
		meshWidth:    2,
		meshHeight:   2,
		centerWorldX: 0,
		centerWorldY: 0,
	}
	layout := terrainTileLayout{
		posX:       -1,
		posZ:       -1,
		tileSpanX:  12,
		tileSpanZ:  12,
		worldWest:  -1,
		worldEast:  11,
		worldNorth: 1,
		worldSouth: -11,
	}
	mainRoad := roadHeightPolygon{
		Points: []roadPoint{{0, 0}, {10, 0}, {10, 10}, {0, 10}},
		Segments: []roadSegment{
			{A: roadPoint{0, 0}, B: roadPoint{10, 0}, Curb: roadCurbHard},
			{A: roadPoint{10, 0}, B: roadPoint{10, 10}, Curb: roadCurbHard},
			{A: roadPoint{10, 10}, B: roadPoint{0, 10}, Curb: roadCurbHard},
			{A: roadPoint{0, 10}, B: roadPoint{0, 0}, Curb: roadCurbSoft},
		},
		Bounds: roadBounds{MinX: 0, MaxX: 10, MinZ: 0, MaxZ: 10},
		Area:   100,
	}
	overlapRoad := roadHeightPolygon{
		Points: []roadPoint{{-0.5, 4}, {2, 4}, {2, 6}, {-0.5, 6}},
		Segments: []roadSegment{
			{A: roadPoint{-0.5, 4}, B: roadPoint{2, 4}, Curb: roadCurbHard},
			{A: roadPoint{2, 4}, B: roadPoint{2, 6}, Curb: roadCurbHard},
			{A: roadPoint{2, 6}, B: roadPoint{-0.5, 6}, Curb: roadCurbHard},
			{A: roadPoint{-0.5, 6}, B: roadPoint{-0.5, 4}, Curb: roadCurbHard},
		},
		Bounds: roadBounds{MinX: -0.5, MaxX: 2, MinZ: 4, MaxZ: 6},
		Area:   5,
	}
	builders := map[int]*roadMeshBuilder{}
	cuts := map[int][]float32{}

	appendRoadSurfaceMeshes(terrain, []terrainTileLayout{layout}, 0, []roadHeightPolygon{mainRoad, overlapRoad}, builders, cuts)

	builder := builders[0]
	if builder == nil {
		t.Fatal("missing road surface builder")
	}
	for i := 0; i+2 < len(builder.vertices); i += 3 {
		x := builder.vertices[i]
		z := builder.vertices[i+2]
		if x < 0 && z > 4 && z < 6 {
			t.Fatalf("soft band vertex in overlapping road mask: x=%.3f z=%.3f", x, z)
		}
	}
}

func TestAppendRoadCutSegments(t *testing.T) {
	layout := terrainTileLayout{
		posX:      10,
		posZ:      20,
		tileSpanX: 20,
		tileSpanZ: 40,
	}
	polygon := []roadPoint{
		{X: 15, Z: 30},
		{X: 25, Z: 30},
		{X: 25, Z: 50},
		{X: 15, Z: 50},
	}
	segments := appendRoadCutSegments(layout, polygon, nil)
	if len(segments) != 4*4 {
		t.Fatalf("got %d floats, want %d", len(segments), 16)
	}
	wantFirst := []float32{0.25, 0.25, 0.75, 0.25}
	for i, want := range wantFirst {
		if math.Abs(float64(segments[i]-want)) > 1e-6 {
			t.Fatalf("segment[%d] = %.6f, want %.6f", i, segments[i], want)
		}
	}
}

func TestRoadCutSegmentsUseNonzeroWindingForOverlaps(t *testing.T) {
	layout := terrainTileLayout{
		posX:      0,
		posZ:      0,
		tileSpanX: 10,
		tileSpanZ: 10,
	}
	a := []roadPoint{
		{X: 1, Z: 1},
		{X: 6, Z: 1},
		{X: 6, Z: 6},
		{X: 1, Z: 6},
	}
	bOppositeOrientation := []roadPoint{
		{X: 4, Z: 4},
		{X: 4, Z: 9},
		{X: 9, Z: 9},
		{X: 9, Z: 4},
	}
	segments := appendRoadCutSegments(layout, a, nil)
	segments = appendRoadCutSegments(layout, bOppositeOrientation, segments)

	if winding := roadCutTestWinding(segments, roadPoint{X: 0.05, Z: 0.05}); winding != 0 {
		t.Fatalf("outside winding = %d, want 0", winding)
	}
	if winding := roadCutTestWinding(segments, roadPoint{X: 0.5, Z: 0.5}); winding == 0 {
		t.Fatal("overlap winding = 0, want nonzero")
	}
}

func roadCutTestWinding(segments []float32, p roadPoint) int {
	winding := 0
	for i := 0; i+3 < len(segments); i += 4 {
		a := roadPoint{X: segments[i], Z: segments[i+1]}
		b := roadPoint{X: segments[i+2], Z: segments[i+3]}
		side := (b.X-a.X)*(p.Z-a.Z) - (b.Z-a.Z)*(p.X-a.X)
		if a.Z <= p.Z {
			if b.Z > p.Z && side > 0 {
				winding++
			}
		} else if b.Z <= p.Z && side < 0 {
			winding--
		}
	}
	return winding
}
