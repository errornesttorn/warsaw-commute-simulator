package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"sort"

	rl "github.com/gen2brain/raylib-go/raylib"
)

const (
	roadSinkMeters               = float32(0.15)
	roadSoftCurbWidthMeters      = float32(0.75)
	roadSurfaceCellMeters        = float32(1.0)
	roadBoundaryMaxSegmentMeters = float32(0.25)
	roadGeomEpsilon              = float32(0.0001)
)

type roadCurbType string

const (
	roadCurbHard roadCurbType = "hard"
	roadCurbSoft roadCurbType = "soft"
)

type roadPoint struct {
	X float32
	Z float32
}

type roadSegment struct {
	A    roadPoint
	B    roadPoint
	Curb roadCurbType
}

type roadHeightPolygon struct {
	ID       int
	Points   []roadPoint
	Segments []roadSegment
	Bounds   roadBounds
	Area     float32
}

type roadBounds struct {
	MinX float32
	MaxX float32
	MinZ float32
	MaxZ float32
}

type roadMeshCPU struct {
	Vertices  []float32
	Normals   []float32
	Texcoords []float32
}

type roadSurfaceMeshCPU struct {
	TileIndex int
	Mesh      roadMeshCPU
}

type roadCutSegmentsCPU struct {
	TileIndex int
	Segments  []float32 // vec4 per segment: ax, ay, bx, by in tile UV space
}

type roadSurfaceCPUData struct {
	Surfaces       []roadSurfaceMeshCPU
	Cuts           []roadCutSegmentsCPU
	HardCurbs      roadMeshCPU
	HeightPolygons []roadHeightPolygon
}

type roadMesh struct {
	Mesh      rl.Mesh
	MeshBytes int64
	Loaded    bool
}

type roadSurfaceMesh struct {
	TileIndex int
	Mesh      rl.Mesh
	MeshBytes int64
	Loaded    bool
}

type roadSurfaceLayer struct {
	Surfaces       []roadSurfaceMesh
	HardCurbs      roadMesh
	HardMaterial   rl.Material
	HeightPolygons []roadHeightPolygon
}

type roadMeshBuilder struct {
	vertices  []float32
	normals   []float32
	texcoords []float32
}

type roadSoftSlopeFrame struct {
	edgeA   roadPoint
	tangent roadPoint
	outward roadPoint
	length  float32
	runPos  float32
	runLen  float32
	capA    bool
	capB    bool
}

type roadMaskFile struct {
	Geotiff struct {
		GeoTransform [6]float64 `json:"geo_transform"`
	} `json:"geotiff"`
	Nodes []roadMaskNode `json:"nodes"`
	Edges []roadMaskEdge `json:"edges"`
}

type roadMaskNode struct {
	ID int     `json:"id"`
	X  float64 `json:"x"`
	Y  float64 `json:"y"`
}

type roadMaskEdge struct {
	ID      int           `json:"id"`
	NodeIDs []int         `json:"node_ids"`
	Segs    []roadMaskSeg `json:"segs"`
}

type roadMaskSeg struct {
	IsSpline bool    `json:"is_spline"`
	Curb     string  `json:"curb"`
	MidX     float64 `json:"mid_x"`
	MidY     float64 `json:"mid_y"`
}

func prepareRoadSurfacesCPU(mapDef *mapDefinition, terrain *terrainData) (*roadSurfaceCPUData, []error) {
	if mapDef == nil || terrain == nil || len(mapDef.RoadMaskPaths) == 0 {
		return nil, nil
	}

	var problems []error
	var polygons []roadHeightPolygon
	for _, path := range mapDef.RoadMaskPaths {
		filePolys, err := loadRoadMaskPolygons(path, terrain)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", filepath.Base(path), err))
			continue
		}
		polygons = append(polygons, filePolys...)
	}
	if len(polygons) == 0 {
		return &roadSurfaceCPUData{}, problems
	}

	layouts := computeTerrainTileLayouts(terrain, terrainTileGridN)
	cpu := &roadSurfaceCPUData{
		HeightPolygons: polygons,
	}
	surfaceBuilders := make(map[int]*roadMeshBuilder)
	cutSegments := make(map[int][]float32)
	for i := range polygons {
		appendRoadSurfaceMeshes(terrain, layouts, i, polygons, surfaceBuilders, cutSegments)
		appendRoadCurbMeshes(terrain, i, polygons, &cpu.HardCurbs)
	}

	tileIndices := make([]int, 0, len(surfaceBuilders))
	for tileIndex := range surfaceBuilders {
		tileIndices = append(tileIndices, tileIndex)
	}
	sort.Ints(tileIndices)
	for _, tileIndex := range tileIndices {
		builder := surfaceBuilders[tileIndex]
		if len(builder.vertices) == 0 {
			continue
		}
		mesh := roadMeshCPU{
			Vertices:  builder.vertices,
			Normals:   builder.normals,
			Texcoords: builder.texcoords,
		}
		cpu.Surfaces = append(cpu.Surfaces, roadSurfaceMeshCPU{
			TileIndex: tileIndex,
			Mesh:      mesh,
		})
	}
	cutTileIndices := make([]int, 0, len(cutSegments))
	for tileIndex := range cutSegments {
		cutTileIndices = append(cutTileIndices, tileIndex)
	}
	sort.Ints(cutTileIndices)
	for _, tileIndex := range cutTileIndices {
		segments := cutSegments[tileIndex]
		if len(segments) == 0 {
			continue
		}
		cpu.Cuts = append(cpu.Cuts, roadCutSegmentsCPU{
			TileIndex: tileIndex,
			Segments:  append([]float32(nil), segments...),
		})
	}
	return cpu, problems
}

func roadHeightOnlyLayer(cpu *roadSurfaceCPUData) *roadSurfaceLayer {
	if cpu == nil || len(cpu.HeightPolygons) == 0 {
		return nil
	}
	return &roadSurfaceLayer{HeightPolygons: cpu.HeightPolygons}
}

func uploadRoadSurfaceLayer(cpu *roadSurfaceCPUData) *roadSurfaceLayer {
	if cpu == nil || (len(cpu.Surfaces) == 0 && len(cpu.HeightPolygons) == 0) {
		return nil
	}

	layer := &roadSurfaceLayer{
		HeightPolygons: cpu.HeightPolygons,
		HardMaterial:   roadMaterial(color.RGBA{R: 54, G: 54, B: 52, A: 255}),
	}

	for _, surface := range cpu.Surfaces {
		mesh, bytes, loaded := uploadRoadMesh(surface.Mesh)
		if !loaded {
			continue
		}
		layer.Surfaces = append(layer.Surfaces, roadSurfaceMesh{
			TileIndex: surface.TileIndex,
			Mesh:      mesh,
			MeshBytes: bytes,
			Loaded:    true,
		})
	}
	if mesh, bytes, loaded := uploadRoadMesh(cpu.HardCurbs); loaded {
		layer.HardCurbs = roadMesh{Mesh: mesh, MeshBytes: bytes, Loaded: true}
	}
	return layer
}

func loadTerrainRoadCutShader() (rl.Shader, int32, bool) {
	shader := rl.LoadShaderFromMemory("", terrainRoadCutFragmentShader)
	if shader.ID == 0 {
		return rl.Shader{}, -1, false
	}
	shader.UpdateLocation(int32(rl.ShaderLocMapAlbedo), rl.GetShaderLocation(shader, "texture0"))
	shader.UpdateLocation(int32(rl.ShaderLocMapEmission), rl.GetShaderLocation(shader, "roadSegments"))
	shader.UpdateLocation(int32(rl.ShaderLocColorDiffuse), rl.GetShaderLocation(shader, "colDiffuse"))
	countLoc := rl.GetShaderLocation(shader, "roadSegmentCount")
	return shader, countLoc, countLoc >= 0
}

func uploadRoadCutSegments(terrain *terrainData, cpu *roadSurfaceCPUData) {
	if terrain == nil || cpu == nil {
		return
	}
	for _, cut := range cpu.Cuts {
		segmentCount := len(cut.Segments) / 4
		if cut.TileIndex < 0 || cut.TileIndex >= len(terrain.tiles) || segmentCount == 0 {
			continue
		}
		pixels := float32SliceBytes(cut.Segments)
		img := rl.NewImage(pixels, int32(segmentCount), 1, 1, rl.UncompressedR32g32b32a32)
		tex := rl.LoadTextureFromImage(img)
		if tex.ID == 0 {
			continue
		}
		rl.SetTextureFilter(tex, rl.FilterPoint)
		rl.SetTextureWrap(tex, rl.WrapClamp)
		terrain.tiles[cut.TileIndex].roadCut = tex
		terrain.tiles[cut.TileIndex].roadCutN = segmentCount
	}
}

func float32SliceBytes(values []float32) []byte {
	out := make([]byte, len(values)*4)
	for i, v := range values {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(v))
	}
	return out
}

func roadMaterial(tint color.RGBA) rl.Material {
	mat := rl.LoadMaterialDefault()
	mat.GetMap(int32(rl.MapAlbedo)).Color = tint
	return mat
}

func uploadRoadMesh(cpu roadMeshCPU) (rl.Mesh, int64, bool) {
	vertexCount := len(cpu.Vertices) / 3
	if vertexCount == 0 || vertexCount%3 != 0 {
		return rl.Mesh{}, 0, false
	}

	mesh := rl.Mesh{
		VertexCount:   int32(vertexCount),
		TriangleCount: int32(vertexCount / 3),
		Vertices:      &cpu.Vertices[0],
	}
	if len(cpu.Normals) == len(cpu.Vertices) {
		mesh.Normals = &cpu.Normals[0]
	}
	if len(cpu.Texcoords) == vertexCount*2 {
		mesh.Texcoords = &cpu.Texcoords[0]
	}
	rl.UploadMesh(&mesh, false)

	mesh.Vertices = nil
	mesh.Normals = nil
	mesh.Texcoords = nil
	meshBytes := int64(len(cpu.Vertices)+len(cpu.Normals)+len(cpu.Texcoords)) * 4
	return mesh, meshBytes, true
}

func unloadRoadSurfaceLayer(layer *roadSurfaceLayer) {
	if layer == nil {
		return
	}
	for i := range layer.Surfaces {
		if layer.Surfaces[i].Loaded {
			rl.UnloadMesh(&layer.Surfaces[i].Mesh)
			layer.Surfaces[i].Loaded = false
		}
	}
	unloadRoadMesh(&layer.HardCurbs)
	if layer.HardMaterial.Maps != nil {
		rl.UnloadMaterial(layer.HardMaterial)
	}
}

func unloadRoadMesh(mesh *roadMesh) {
	if mesh != nil && mesh.Loaded {
		rl.UnloadMesh(&mesh.Mesh)
		mesh.Loaded = false
	}
}

func drawTerrainWithRoadCuts(t *terrainData) {
	drawTerrainTiles(t)
}

func drawRoadSurfaceLayer(t *terrainData) {
	if t == nil || t.roads == nil {
		return
	}
	rl.DisableBackfaceCulling()
	for _, surface := range t.roads.Surfaces {
		if !surface.Loaded || surface.TileIndex < 0 || surface.TileIndex >= len(t.tiles) {
			continue
		}
		rl.DrawMesh(surface.Mesh, t.tiles[surface.TileIndex].material, rl.MatrixIdentity())
	}
	if t.roads.HardCurbs.Loaded {
		rl.DrawMesh(t.roads.HardCurbs.Mesh, t.roads.HardMaterial, rl.MatrixIdentity())
	}
	rl.EnableBackfaceCulling()
}

const terrainRoadCutFragmentShader = `#version 330
in vec2 fragTexCoord;
in vec4 fragColor;
uniform sampler2D texture0;
uniform sampler2D roadSegments;
uniform float roadSegmentCount;
uniform vec4 colDiffuse;
out vec4 finalColor;
void main() {
    int winding = 0;
    int count = int(roadSegmentCount + 0.5);
    for (int i = 0; i < count; i++) {
        vec4 seg = texelFetch(roadSegments, ivec2(i, 0), 0);
        vec2 a = seg.xy;
        vec2 b = seg.zw;
        float side = (b.x - a.x) * (fragTexCoord.y - a.y) - (b.y - a.y) * (fragTexCoord.x - a.x);
        if (a.y <= fragTexCoord.y) {
            if (b.y > fragTexCoord.y && side > 0.0) {
                winding++;
            }
        } else if (b.y <= fragTexCoord.y && side < 0.0) {
            winding--;
        }
    }
    if (winding != 0) {
        discard;
    }
    finalColor = texture(texture0, fragTexCoord) * colDiffuse * fragColor;
}
`

func (layer *roadSurfaceLayer) heightAtLocal(localX, localZ, base float32) float32 {
	if layer == nil {
		return base
	}
	found := false
	best := base
	p := roadPoint{X: localX, Z: localZ}
	for _, polygon := range layer.HeightPolygons {
		if !polygon.Bounds.expanded(roadSoftCurbWidthMeters).contains(p) {
			continue
		}
		distToEdge := roadDistanceToSegments(p, polygon.Segments)
		insideRoad := pointInRoadPolygon(p, polygon.Points) || distToEdge <= roadGeomEpsilon
		offset := float32(0)
		switch {
		case insideRoad:
			offset = -roadSinkMeters
		default:
			influence := roadSoftCurbInfluenceAtPoint(p, polygon)
			if influence <= 0 {
				continue
			}
			offset = -roadSinkMeters * influence
		}
		h := base + offset
		if !found || h < best {
			best = h
			found = true
		}
	}
	if !found {
		return base
	}
	return best
}

func loadRoadMaskPolygons(path string, terrain *terrainData) ([]roadHeightPolygon, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var doc roadMaskFile
	if err := json.NewDecoder(file).Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode road mask: %w", err)
	}
	if doc.Geotiff.GeoTransform[1] == 0 && doc.Geotiff.GeoTransform[5] == 0 {
		return nil, errors.New("missing geotiff.geo_transform")
	}

	nodes := make(map[int]roadMaskNode, len(doc.Nodes))
	for _, node := range doc.Nodes {
		nodes[node.ID] = node
	}

	polygons := make([]roadHeightPolygon, 0, len(doc.Edges))
	for _, edge := range doc.Edges {
		if len(edge.NodeIDs) < 4 {
			return nil, fmt.Errorf("edge %d needs at least 4 node ids", edge.ID)
		}
		if len(edge.Segs) != len(edge.NodeIDs)-1 {
			return nil, fmt.Errorf("edge %d has %d segments for %d node ids", edge.ID, len(edge.Segs), len(edge.NodeIDs))
		}

		var points []roadPoint
		var segments []roadSegment
		for i, seg := range edge.Segs {
			aNode, ok := nodes[edge.NodeIDs[i]]
			if !ok {
				return nil, fmt.Errorf("edge %d references missing node %d", edge.ID, edge.NodeIDs[i])
			}
			bNode, ok := nodes[edge.NodeIDs[i+1]]
			if !ok {
				return nil, fmt.Errorf("edge %d references missing node %d", edge.ID, edge.NodeIDs[i+1])
			}
			curb, err := parseRoadCurbType(seg.Curb)
			if err != nil {
				return nil, fmt.Errorf("edge %d segment %d: %w", edge.ID, i, err)
			}
			segPoints := tessellateRoadMaskSegment(doc.Geotiff.GeoTransform, terrain, aNode, bNode, seg)
			if len(segPoints) < 2 {
				continue
			}
			if len(points) == 0 {
				points = append(points, segPoints[0])
			}
			for j := 1; j < len(segPoints); j++ {
				prev := segPoints[j-1]
				next := segPoints[j]
				if roadPointsNear(prev, next) {
					continue
				}
				points = append(points, next)
				segments = append(segments, roadSegment{A: prev, B: next, Curb: curb})
			}
		}

		points = cleanRoadPolygon(points)
		if len(points) < 3 {
			continue
		}
		bounds := roadPolygonBounds(points)
		area := roadPolygonArea(points)
		if float32(math.Abs(float64(area))) < roadGeomEpsilon {
			continue
		}
		polygons = append(polygons, roadHeightPolygon{
			ID:       edge.ID,
			Points:   points,
			Segments: segments,
			Bounds:   bounds,
			Area:     area,
		})
	}
	return polygons, nil
}

func parseRoadCurbType(value string) (roadCurbType, error) {
	switch roadCurbType(value) {
	case "", roadCurbHard:
		return roadCurbHard, nil
	case roadCurbSoft:
		return roadCurbSoft, nil
	default:
		return "", fmt.Errorf("unsupported curb type %q", value)
	}
}

func tessellateRoadMaskSegment(gt [6]float64, terrain *terrainData, aNode, bNode roadMaskNode, seg roadMaskSeg) []roadPoint {
	a := roadMaskPixelToLocal(gt, terrain, aNode.X, aNode.Y)
	b := roadMaskPixelToLocal(gt, terrain, bNode.X, bNode.Y)
	if !seg.IsSpline {
		return []roadPoint{a, b}
	}

	ctrl := roadMaskPixelToLocal(gt, terrain, seg.MidX, seg.MidY)
	estimate := roadPointDistance(a, ctrl) + roadPointDistance(ctrl, b)
	steps := int(math.Ceil(float64(estimate / roadBoundaryMaxSegmentMeters)))
	if steps < 4 {
		steps = 4
	}
	out := make([]roadPoint, 0, steps+1)
	for i := 0; i <= steps; i++ {
		t := float32(i) / float32(steps)
		mt := 1 - t
		out = append(out, roadPoint{
			X: mt*mt*a.X + 2*mt*t*ctrl.X + t*t*b.X,
			Z: mt*mt*a.Z + 2*mt*t*ctrl.Z + t*t*b.Z,
		})
	}
	return out
}

func roadMaskPixelToLocal(gt [6]float64, terrain *terrainData, pixelX, pixelY float64) roadPoint {
	worldX := gt[0] + pixelX*gt[1] + pixelY*gt[2]
	worldY := gt[3] + pixelX*gt[4] + pixelY*gt[5]
	return roadPoint{
		X: float32(worldX - terrain.centerWorldX),
		Z: float32(terrain.centerWorldY - worldY),
	}
}

func appendRoadSurfaceMeshes(terrain *terrainData, layouts []terrainTileLayout, polygonIndex int, allPolygons []roadHeightPolygon, builders map[int]*roadMeshBuilder, cuts map[int][]float32) {
	if polygonIndex < 0 || polygonIndex >= len(allPolygons) {
		return
	}
	polygon := allPolygons[polygonIndex]
	ccw := polygon.Area > 0
	renderBounds := polygon.Bounds.expanded(roadSoftCurbWidthMeters)
	for tileIndex, layout := range layouts {
		tileMinX := layout.posX
		tileMaxX := layout.posX + layout.tileSpanX
		tileMinZ := layout.posZ
		tileMaxZ := layout.posZ + layout.tileSpanZ
		tileBounds := roadBounds{MinX: tileMinX, MaxX: tileMaxX, MinZ: tileMinZ, MaxZ: tileMaxZ}
		if !renderBounds.intersects(tileBounds) {
			continue
		}

		tilePoly := clipRoadPolygonToRect(polygon.Points, tileMinX, tileMaxX, tileMinZ, tileMaxZ)
		builder := builders[tileIndex]
		if len(tilePoly) >= 3 {
			cuts[tileIndex] = appendRoadCutSegments(layout, polygon.Points, cuts[tileIndex])
			builder = roadBuilderForTile(builders, tileIndex)
			appendRoadFlatSurfaceMeshForTile(terrain, layout, tilePoly, builder)
		}

		for segmentIndex, segment := range polygon.Segments {
			if segment.Curb != roadCurbSoft {
				continue
			}
			stripPoly, ok := roadSoftCurbStripPolygon(segment, ccw)
			if !ok || !roadPolygonBounds(stripPoly).intersects(tileBounds) {
				continue
			}
			clippedStrip := clipRoadPolygonToRect(stripPoly, tileMinX, tileMaxX, tileMinZ, tileMaxZ)
			if len(clippedStrip) < 3 {
				continue
			}
			builder = roadBuilderForTile(builders, tileIndex)
			cuts[tileIndex] = appendRoadSoftSlopeMeshForTile(terrain, layout, tileBounds, polygonIndex, allPolygons, segmentIndex, builder, cuts[tileIndex])
			appendRoadSoftEdgeFillerMeshForTile(terrain, layout, tileBounds, polygonIndex, allPolygons, segmentIndex, builder)
		}
	}
}

func roadBuilderForTile(builders map[int]*roadMeshBuilder, tileIndex int) *roadMeshBuilder {
	builder := builders[tileIndex]
	if builder == nil {
		builder = &roadMeshBuilder{}
		builders[tileIndex] = builder
	}
	return builder
}

func appendRoadFlatSurfaceMeshForTile(terrain *terrainData, layout terrainTileLayout, tilePoly []roadPoint, builder *roadMeshBuilder) {
	tileBounds := roadPolygonBounds(tilePoly)
	startX := float32(math.Floor(float64(tileBounds.MinX/roadSurfaceCellMeters))) * roadSurfaceCellMeters
	startZ := float32(math.Floor(float64(tileBounds.MinZ/roadSurfaceCellMeters))) * roadSurfaceCellMeters
	for x := startX; x < tileBounds.MaxX-roadGeomEpsilon; x += roadSurfaceCellMeters {
		for z := startZ; z < tileBounds.MaxZ-roadGeomEpsilon; z += roadSurfaceCellMeters {
			cellPoly := clipRoadPolygonToRect(
				tilePoly,
				max32(x, layout.posX),
				min32(x+roadSurfaceCellMeters, layout.posX+layout.tileSpanX),
				max32(z, layout.posZ),
				min32(z+roadSurfaceCellMeters, layout.posZ+layout.tileSpanZ),
			)
			if len(cellPoly) < 3 {
				continue
			}
			if math.Abs(float64(roadPolygonArea(cellPoly))) < float64(roadGeomEpsilon) {
				continue
			}
			tris := triangulateRoadPolygon(cellPoly)
			for _, tri := range tris {
				a := cellPoly[tri[0]]
				b := cellPoly[tri[1]]
				c := cellPoly[tri[2]]
				appendRoadSurfaceTriangle(terrain, layout, builder, a, b, c)
			}
		}
	}
}

func appendRoadSoftSlopeMeshForTile(terrain *terrainData, layout terrainTileLayout, tileBounds roadBounds, polygonIndex int, allPolygons []roadHeightPolygon, segmentIndex int, builder *roadMeshBuilder, cutOut []float32) []float32 {
	if polygonIndex < 0 || polygonIndex >= len(allPolygons) {
		return cutOut
	}
	polygon := allPolygons[polygonIndex]
	if segmentIndex < 0 || segmentIndex >= len(polygon.Segments) {
		return cutOut
	}
	segment := polygon.Segments[segmentIndex]
	frame, ok := roadSoftSlopeFrameForPolygonSegment(polygon, segmentIndex)
	if !ok {
		return cutOut
	}
	ccw := polygon.Area > 0
	for _, piece := range roadSubdivideSegment(segment, roadBoundaryMaxSegmentMeters) {
		if roadSoftBandPieceCoveredByOtherRoad(piece, ccw, polygonIndex, allPolygons) {
			continue
		}
		stripPoly, ok := roadSoftCurbStripPolygon(piece, ccw)
		if !ok || !roadPolygonBounds(stripPoly).intersects(tileBounds) {
			continue
		}
		clipped := clipRoadPolygonToRect(stripPoly, tileBounds.MinX, tileBounds.MaxX, tileBounds.MinZ, tileBounds.MaxZ)
		if len(clipped) < 3 {
			continue
		}
		if math.Abs(float64(roadPolygonArea(clipped))) < float64(roadGeomEpsilon) {
			continue
		}
		cutOut = appendRoadCutSegments(layout, stripPoly, cutOut)
		tris := triangulateRoadPolygon(clipped)
		for _, tri := range tris {
			a := clipped[tri[0]]
			b := clipped[tri[1]]
			c := clipped[tri[2]]
			appendRoadSoftSlopeTriangle(terrain, layout, builder, frame, a, b, c)
		}
	}
	return cutOut
}

func appendRoadSoftEdgeFillerMeshForTile(terrain *terrainData, layout terrainTileLayout, tileBounds roadBounds, polygonIndex int, allPolygons []roadHeightPolygon, segmentIndex int, builder *roadMeshBuilder) {
	if polygonIndex < 0 || polygonIndex >= len(allPolygons) {
		return
	}
	polygon := allPolygons[polygonIndex]
	if segmentIndex < 0 || segmentIndex >= len(polygon.Segments) {
		return
	}
	segment := polygon.Segments[segmentIndex]
	frame, ok := roadSoftSlopeFrameForPolygonSegment(polygon, segmentIndex)
	if !ok {
		return
	}
	ccw := polygon.Area > 0
	for _, piece := range roadSubdivideSegment(segment, roadBoundaryMaxSegmentMeters) {
		if roadSoftBandPieceCoveredByOtherRoad(piece, ccw, polygonIndex, allPolygons) {
			continue
		}
		a, b, ok := clipRoadSegmentToRect(piece.A, piece.B, tileBounds.MinX, tileBounds.MaxX, tileBounds.MinZ, tileBounds.MaxZ)
		if !ok || roadPointsNear(a, b) {
			continue
		}
		appendRoadSoftEdgeFillerQuad(terrain, layout, builder, frame, a, b)
	}
}

func appendRoadSoftEdgeFillerQuad(terrain *terrainData, layout terrainTileLayout, builder *roadMeshBuilder, frame roadSoftSlopeFrame, a, b roadPoint) {
	aRoadY := terrainBaseHeightAtLocal(terrain, a.X, a.Z) - roadSinkMeters
	bRoadY := terrainBaseHeightAtLocal(terrain, b.X, b.Z) - roadSinkMeters
	aSoftY := roadSoftSlopeHeightAtLocal(terrain, a, frame)
	bSoftY := roadSoftSlopeHeightAtLocal(terrain, b, frame)
	if math.Abs(float64(aSoftY-aRoadY)) <= float64(roadGeomEpsilon) &&
		math.Abs(float64(bSoftY-bRoadY)) <= float64(roadGeomEpsilon) {
		return
	}
	appendRoadTexturedTriangle(terrain, layout, builder, a, aSoftY, b, bSoftY, b, bRoadY)
	appendRoadTexturedTriangle(terrain, layout, builder, a, aSoftY, b, bRoadY, a, aRoadY)
}

func roadSoftBandPieceCoveredByOtherRoad(piece roadSegment, ccw bool, polygonIndex int, allPolygons []roadHeightPolygon) bool {
	outward, ok := roadSegmentOutwardNormal(piece, ccw)
	if !ok {
		return true
	}
	midEdge := roadLerpPoint(piece.A, piece.B, 0.5)
	midStrip := roadPoint{
		X: midEdge.X + outward.X*roadSoftCurbWidthMeters*0.5,
		Z: midEdge.Z + outward.Z*roadSoftCurbWidthMeters*0.5,
	}
	edgeA := roadPoint{
		X: piece.A.X + outward.X*roadGeomEpsilon*4,
		Z: piece.A.Z + outward.Z*roadGeomEpsilon*4,
	}
	edgeB := roadPoint{
		X: piece.B.X + outward.X*roadGeomEpsilon*4,
		Z: piece.B.Z + outward.Z*roadGeomEpsilon*4,
	}
	for i, other := range allPolygons {
		if i == polygonIndex {
			continue
		}
		if !other.Bounds.expanded(roadGeomEpsilon).contains(midEdge) &&
			!other.Bounds.expanded(roadSoftCurbWidthMeters).contains(midStrip) {
			continue
		}
		if roadPointCoveredByRoadPolygon(midEdge, other) ||
			roadPointCoveredByRoadPolygon(midStrip, other) ||
			roadPointCoveredByRoadPolygon(edgeA, other) ||
			roadPointCoveredByRoadPolygon(edgeB, other) {
			return true
		}
	}
	return false
}

func roadPointCoveredByRoadPolygon(p roadPoint, polygon roadHeightPolygon) bool {
	return polygon.Bounds.expanded(roadGeomEpsilon).contains(p) &&
		(pointInRoadPolygon(p, polygon.Points) || roadDistanceToSegments(p, polygon.Segments) <= roadGeomEpsilon)
}

func appendRoadSoftSlopeTriangle(terrain *terrainData, layout terrainTileLayout, builder *roadMeshBuilder, frame roadSoftSlopeFrame, a, b, c roadPoint) {
	ay := roadSoftSlopeHeightAtLocal(terrain, a, frame)
	by := roadSoftSlopeHeightAtLocal(terrain, b, frame)
	cy := roadSoftSlopeHeightAtLocal(terrain, c, frame)
	appendRoadTexturedTriangle(terrain, layout, builder, a, ay, b, by, c, cy)
}

func roadSoftSlopeHeightAtLocal(terrain *terrainData, p roadPoint, frame roadSoftSlopeFrame) float32 {
	return terrainBaseHeightAtLocal(terrain, p.X, p.Z) - roadSinkMeters*roadSoftSlopeInfluence(frame, p)
}

func roadSoftCurbStripPolygon(segment roadSegment, ccw bool) ([]roadPoint, bool) {
	outward, ok := roadSegmentOutwardNormal(segment, ccw)
	if !ok {
		return nil, false
	}
	aOuter := roadPoint{
		X: segment.A.X + outward.X*roadSoftCurbWidthMeters,
		Z: segment.A.Z + outward.Z*roadSoftCurbWidthMeters,
	}
	bOuter := roadPoint{
		X: segment.B.X + outward.X*roadSoftCurbWidthMeters,
		Z: segment.B.Z + outward.Z*roadSoftCurbWidthMeters,
	}
	return []roadPoint{segment.A, segment.B, bOuter, aOuter}, true
}

func roadSegmentOutwardNormal(segment roadSegment, ccw bool) (roadPoint, bool) {
	dx := segment.B.X - segment.A.X
	dz := segment.B.Z - segment.A.Z
	l := float32(math.Sqrt(float64(dx*dx + dz*dz)))
	if l <= roadGeomEpsilon {
		return roadPoint{}, false
	}
	left := roadPoint{X: -dz / l, Z: dx / l}
	if ccw {
		return roadPoint{X: -left.X, Z: -left.Z}, true
	}
	return left, true
}

func roadSoftSlopeFrameForSegment(segment roadSegment, ccw bool, runPos, runLen float32, capA, capB bool) (roadSoftSlopeFrame, bool) {
	dx := segment.B.X - segment.A.X
	dz := segment.B.Z - segment.A.Z
	length := float32(math.Sqrt(float64(dx*dx + dz*dz)))
	if length <= roadGeomEpsilon {
		return roadSoftSlopeFrame{}, false
	}
	outward, ok := roadSegmentOutwardNormal(segment, ccw)
	if !ok {
		return roadSoftSlopeFrame{}, false
	}
	return roadSoftSlopeFrame{
		edgeA:   segment.A,
		tangent: roadPoint{X: dx / length, Z: dz / length},
		outward: outward,
		length:  length,
		runPos:  runPos,
		runLen:  runLen,
		capA:    capA,
		capB:    capB,
	}, true
}

func roadSoftSlopeFrameForPolygonSegment(polygon roadHeightPolygon, segmentIndex int) (roadSoftSlopeFrame, bool) {
	n := len(polygon.Segments)
	if segmentIndex < 0 || segmentIndex >= n {
		return roadSoftSlopeFrame{}, false
	}
	segment := polygon.Segments[segmentIndex]
	if segment.Curb != roadCurbSoft {
		return roadSoftSlopeFrame{}, false
	}
	if roadSoftCurbRunClosed(polygon) {
		return roadSoftSlopeFrameForSegment(segment, polygon.Area > 0, 0, roadSoftCurbTotalLength(polygon), false, false)
	}

	start := segmentIndex
	for {
		prevIndex := (start + n - 1) % n
		prev := polygon.Segments[prevIndex]
		curr := polygon.Segments[start]
		if prev.Curb != roadCurbSoft || !roadPointsNear(prev.B, curr.A) {
			break
		}
		start = prevIndex
	}

	end := segmentIndex
	for {
		nextIndex := (end + 1) % n
		curr := polygon.Segments[end]
		next := polygon.Segments[nextIndex]
		if next.Curb != roadCurbSoft || !roadPointsNear(curr.B, next.A) {
			break
		}
		end = nextIndex
	}

	runPos := float32(0)
	runLen := float32(0)
	for i := start; ; i = (i + 1) % n {
		l := roadSegmentLength(polygon.Segments[i])
		if i == segmentIndex {
			runPos = runLen
		}
		runLen += l
		if i == end {
			break
		}
	}
	return roadSoftSlopeFrameForSegment(segment, polygon.Area > 0, runPos, runLen, true, true)
}

func roadSoftCurbRunClosed(polygon roadHeightPolygon) bool {
	if len(polygon.Segments) == 0 {
		return false
	}
	for i, segment := range polygon.Segments {
		next := polygon.Segments[(i+1)%len(polygon.Segments)]
		if segment.Curb != roadCurbSoft || next.Curb != roadCurbSoft || !roadPointsNear(segment.B, next.A) {
			return false
		}
	}
	return true
}

func roadSoftCurbTotalLength(polygon roadHeightPolygon) float32 {
	var total float32
	for _, segment := range polygon.Segments {
		if segment.Curb == roadCurbSoft {
			total += roadSegmentLength(segment)
		}
	}
	return total
}

func roadSoftCurbInfluenceAtPoint(p roadPoint, polygon roadHeightPolygon) float32 {
	best := float32(0)
	for i, segment := range polygon.Segments {
		if segment.Curb != roadCurbSoft {
			continue
		}
		frame, ok := roadSoftSlopeFrameForPolygonSegment(polygon, i)
		if !ok {
			continue
		}
		best = max32(best, roadSoftSlopeInfluence(frame, p))
	}
	return best
}

func roadSoftSlopeInfluence(frame roadSoftSlopeFrame, p roadPoint) float32 {
	rel := roadPoint{X: p.X - frame.edgeA.X, Z: p.Z - frame.edgeA.Z}
	along := roadDot(rel, frame.tangent)
	outward := roadDot(rel, frame.outward)
	if outward < -roadGeomEpsilon || outward > roadSoftCurbWidthMeters+roadGeomEpsilon ||
		along < -roadGeomEpsilon || along > frame.length+roadGeomEpsilon {
		return 0
	}
	acrossT := clamp32(outward/roadSoftCurbWidthMeters, 0, 1)
	endCap := float32(1)
	runAlong := frame.runPos + along
	if frame.capA {
		leftT := clamp32(runAlong/roadSoftCurbWidthMeters, 0, 1)
		endCap = min32(endCap, roadSmoothstep(leftT))
	}
	if frame.capB {
		rightT := clamp32((frame.runLen-runAlong)/roadSoftCurbWidthMeters, 0, 1)
		endCap = min32(endCap, roadSmoothstep(rightT))
	}
	return (1 - acrossT) * endCap
}

func roadSmoothstep(t float32) float32 {
	t = clamp32(t, 0, 1)
	return t * t * (3 - 2*t)
}

func appendRoadCutSegments(layout terrainTileLayout, polygon []roadPoint, out []float32) []float32 {
	if len(polygon) < 3 || layout.tileSpanX <= 0 || layout.tileSpanZ <= 0 {
		return out
	}
	polygon = cleanRoadPolygon(polygon)
	if roadPolygonArea(polygon) < 0 {
		polygon = reverseRoadPolygon(polygon)
	}
	for i := range polygon {
		a := polygon[i]
		b := polygon[(i+1)%len(polygon)]
		if roadPointsNear(a, b) {
			continue
		}
		out = append(out,
			(a.X-layout.posX)/layout.tileSpanX,
			(a.Z-layout.posZ)/layout.tileSpanZ,
			(b.X-layout.posX)/layout.tileSpanX,
			(b.Z-layout.posZ)/layout.tileSpanZ,
		)
	}
	return out
}

func reverseRoadPolygon(poly []roadPoint) []roadPoint {
	out := make([]roadPoint, len(poly))
	for i := range poly {
		out[i] = poly[len(poly)-1-i]
	}
	return out
}

func appendRoadSurfaceTriangle(terrain *terrainData, layout terrainTileLayout, builder *roadMeshBuilder, a, b, c roadPoint) {
	appendRoadSurfaceVertex(terrain, layout, builder, a)
	appendRoadSurfaceVertex(terrain, layout, builder, b)
	appendRoadSurfaceVertex(terrain, layout, builder, c)
}

func appendRoadSurfaceVertex(terrain *terrainData, layout terrainTileLayout, builder *roadMeshBuilder, p roadPoint) {
	y := terrainBaseHeightAtLocal(terrain, p.X, p.Z) - roadSinkMeters
	worldX := terrain.centerWorldX + float64(p.X)
	worldY := terrain.centerWorldY - float64(p.Z)
	u := float32((worldX - layout.worldWest) / (layout.worldEast - layout.worldWest))
	v := float32((layout.worldNorth - worldY) / (layout.worldNorth - layout.worldSouth))
	builder.vertices = append(builder.vertices, p.X, y, p.Z)
	builder.normals = append(builder.normals, 0, 1, 0)
	builder.texcoords = append(builder.texcoords, u, v)
}

func appendRoadTexturedTriangle(terrain *terrainData, layout terrainTileLayout, builder *roadMeshBuilder, a roadPoint, ay float32, b roadPoint, by float32, c roadPoint, cy float32) {
	normal := roadTriangleNormal(
		rl.NewVector3(a.X, ay, a.Z),
		rl.NewVector3(b.X, by, b.Z),
		rl.NewVector3(c.X, cy, c.Z),
	)
	if normal.Y < 0 {
		normal = rl.NewVector3(-normal.X, -normal.Y, -normal.Z)
	}
	appendRoadTexturedVertex(terrain, layout, builder, a, ay, normal)
	appendRoadTexturedVertex(terrain, layout, builder, b, by, normal)
	appendRoadTexturedVertex(terrain, layout, builder, c, cy, normal)
}

func appendRoadTexturedVertex(terrain *terrainData, layout terrainTileLayout, builder *roadMeshBuilder, p roadPoint, y float32, normal rl.Vector3) {
	worldX := terrain.centerWorldX + float64(p.X)
	worldY := terrain.centerWorldY - float64(p.Z)
	u := float32((worldX - layout.worldWest) / (layout.worldEast - layout.worldWest))
	v := float32((layout.worldNorth - worldY) / (layout.worldNorth - layout.worldSouth))
	builder.vertices = append(builder.vertices, p.X, y, p.Z)
	builder.normals = append(builder.normals, normal.X, normal.Y, normal.Z)
	builder.texcoords = append(builder.texcoords, u, v)
}

func appendRoadCurbMeshes(terrain *terrainData, polygonIndex int, allPolygons []roadHeightPolygon, hard *roadMeshCPU) {
	if polygonIndex < 0 || polygonIndex >= len(allPolygons) {
		return
	}
	polygon := allPolygons[polygonIndex]
	for _, segment := range polygon.Segments {
		if segment.Curb == roadCurbHard {
			appendHardCurbSegment(terrain, segment, polygonIndex, allPolygons, hard)
		}
	}
}

func appendHardCurbSegment(terrain *terrainData, segment roadSegment, polygonIndex int, allPolygons []roadHeightPolygon, mesh *roadMeshCPU) {
	for _, piece := range roadSubdivideSegment(segment, roadBoundaryMaxSegmentMeters) {
		if roadCurbPieceCoveredByOtherRoad(piece, polygonIndex, allPolygons) {
			continue
		}
		aTopY := terrainBaseHeightAtLocal(terrain, piece.A.X, piece.A.Z)
		bTopY := terrainBaseHeightAtLocal(terrain, piece.B.X, piece.B.Z)
		aBottomY := aTopY - roadSinkMeters
		bBottomY := bTopY - roadSinkMeters
		appendRoadColorQuad(mesh,
			rl.NewVector3(piece.A.X, aTopY, piece.A.Z),
			rl.NewVector3(piece.B.X, bTopY, piece.B.Z),
			rl.NewVector3(piece.B.X, bBottomY, piece.B.Z),
			rl.NewVector3(piece.A.X, aBottomY, piece.A.Z),
		)
	}
}

func roadCurbPieceCoveredByOtherRoad(piece roadSegment, polygonIndex int, allPolygons []roadHeightPolygon) bool {
	mid := roadLerpPoint(piece.A, piece.B, 0.5)
	for i, other := range allPolygons {
		if i == polygonIndex {
			continue
		}
		if roadPointCoveredByRoadPolygon(mid, other) ||
			roadPointCoveredByRoadPolygon(piece.A, other) ||
			roadPointCoveredByRoadPolygon(piece.B, other) {
			return true
		}
	}
	return false
}

func roadSubdivideSegment(segment roadSegment, maxLen float32) []roadSegment {
	if maxLen <= roadGeomEpsilon {
		maxLen = roadBoundaryMaxSegmentMeters
	}
	length := roadSegmentLength(segment)
	steps := int(math.Ceil(float64(length / maxLen)))
	if steps < 1 {
		steps = 1
	}
	out := make([]roadSegment, 0, steps)
	for i := 0; i < steps; i++ {
		t0 := float32(i) / float32(steps)
		t1 := float32(i+1) / float32(steps)
		out = append(out, roadSegment{
			A:    roadLerpPoint(segment.A, segment.B, t0),
			B:    roadLerpPoint(segment.A, segment.B, t1),
			Curb: segment.Curb,
		})
	}
	return out
}

func roadSegmentLength(segment roadSegment) float32 {
	return roadPointDistance(segment.A, segment.B)
}

func roadLerpPoint(a, b roadPoint, t float32) roadPoint {
	return roadPoint{
		X: a.X + (b.X-a.X)*t,
		Z: a.Z + (b.Z-a.Z)*t,
	}
}

func appendRoadColorQuad(mesh *roadMeshCPU, a, b, c, d rl.Vector3) {
	appendRoadColorTriangle(mesh, a, b, c)
	appendRoadColorTriangle(mesh, a, c, d)
}

func appendRoadColorTriangle(mesh *roadMeshCPU, a, b, c rl.Vector3) {
	n := roadTriangleNormal(a, b, c)
	for _, p := range []rl.Vector3{a, b, c} {
		mesh.Vertices = append(mesh.Vertices, p.X, p.Y, p.Z)
		mesh.Normals = append(mesh.Normals, n.X, n.Y, n.Z)
	}
}

func roadTriangleNormal(a, b, c rl.Vector3) rl.Vector3 {
	ux, uy, uz := b.X-a.X, b.Y-a.Y, b.Z-a.Z
	vx, vy, vz := c.X-a.X, c.Y-a.Y, c.Z-a.Z
	nx := uy*vz - uz*vy
	ny := uz*vx - ux*vz
	nz := ux*vy - uy*vx
	l := float32(math.Sqrt(float64(nx*nx + ny*ny + nz*nz)))
	if l <= roadGeomEpsilon {
		return rl.NewVector3(0, 1, 0)
	}
	return rl.NewVector3(nx/l, ny/l, nz/l)
}

func clipRoadPolygonToRect(poly []roadPoint, minX, maxX, minZ, maxZ float32) []roadPoint {
	out := cleanRoadPolygon(poly)
	out = clipRoadPolygonEdge(out, func(p roadPoint) bool { return p.X >= minX-roadGeomEpsilon }, func(a, b roadPoint) roadPoint {
		return roadIntersectVertical(a, b, minX)
	})
	out = clipRoadPolygonEdge(out, func(p roadPoint) bool { return p.X <= maxX+roadGeomEpsilon }, func(a, b roadPoint) roadPoint {
		return roadIntersectVertical(a, b, maxX)
	})
	out = clipRoadPolygonEdge(out, func(p roadPoint) bool { return p.Z >= minZ-roadGeomEpsilon }, func(a, b roadPoint) roadPoint {
		return roadIntersectHorizontal(a, b, minZ)
	})
	out = clipRoadPolygonEdge(out, func(p roadPoint) bool { return p.Z <= maxZ+roadGeomEpsilon }, func(a, b roadPoint) roadPoint {
		return roadIntersectHorizontal(a, b, maxZ)
	})
	return cleanRoadPolygon(out)
}

func clipRoadPolygonEdge(poly []roadPoint, inside func(roadPoint) bool, intersect func(roadPoint, roadPoint) roadPoint) []roadPoint {
	if len(poly) == 0 {
		return nil
	}
	out := make([]roadPoint, 0, len(poly)+2)
	prev := poly[len(poly)-1]
	prevInside := inside(prev)
	for _, curr := range poly {
		currInside := inside(curr)
		switch {
		case currInside && prevInside:
			out = append(out, curr)
		case currInside && !prevInside:
			out = append(out, intersect(prev, curr), curr)
		case !currInside && prevInside:
			out = append(out, intersect(prev, curr))
		}
		prev = curr
		prevInside = currInside
	}
	return out
}

func roadIntersectVertical(a, b roadPoint, x float32) roadPoint {
	denom := b.X - a.X
	if float32(math.Abs(float64(denom))) <= roadGeomEpsilon {
		return roadPoint{X: x, Z: a.Z}
	}
	t := (x - a.X) / denom
	return roadPoint{X: x, Z: a.Z + (b.Z-a.Z)*t}
}

func roadIntersectHorizontal(a, b roadPoint, z float32) roadPoint {
	denom := b.Z - a.Z
	if float32(math.Abs(float64(denom))) <= roadGeomEpsilon {
		return roadPoint{X: a.X, Z: z}
	}
	t := (z - a.Z) / denom
	return roadPoint{X: a.X + (b.X-a.X)*t, Z: z}
}

func clipRoadSegmentToRect(a, b roadPoint, minX, maxX, minZ, maxZ float32) (roadPoint, roadPoint, bool) {
	t0 := float32(0)
	t1 := float32(1)
	dx := b.X - a.X
	dz := b.Z - a.Z
	if !clipRoadSegmentParam(-dx, a.X-minX, &t0, &t1) ||
		!clipRoadSegmentParam(dx, maxX-a.X, &t0, &t1) ||
		!clipRoadSegmentParam(-dz, a.Z-minZ, &t0, &t1) ||
		!clipRoadSegmentParam(dz, maxZ-a.Z, &t0, &t1) {
		return roadPoint{}, roadPoint{}, false
	}
	return roadLerpPoint(a, b, t0), roadLerpPoint(a, b, t1), true
}

func clipRoadSegmentParam(p, q float32, t0, t1 *float32) bool {
	if math.Abs(float64(p)) <= float64(roadGeomEpsilon) {
		return q >= -roadGeomEpsilon
	}
	r := q / p
	if p < 0 {
		if r > *t1 {
			return false
		}
		if r > *t0 {
			*t0 = r
		}
		return true
	}
	if r < *t0 {
		return false
	}
	if r < *t1 {
		*t1 = r
	}
	return true
}

func triangulateRoadPolygon(poly []roadPoint) [][3]int {
	poly = cleanRoadPolygon(poly)
	if len(poly) < 3 {
		return nil
	}
	area := roadPolygonArea(poly)
	if math.Abs(float64(area)) < float64(roadGeomEpsilon) {
		return nil
	}
	ccw := area > 0
	indices := make([]int, len(poly))
	for i := range indices {
		indices[i] = i
	}

	tris := make([][3]int, 0, len(poly)-2)
	guard := 0
	for len(indices) > 3 && guard < len(poly)*len(poly) {
		earFound := false
		for i := range indices {
			prev := indices[(i+len(indices)-1)%len(indices)]
			curr := indices[i]
			next := indices[(i+1)%len(indices)]
			if !roadIsConvex(poly[prev], poly[curr], poly[next], ccw) {
				continue
			}
			contains := false
			for _, idx := range indices {
				if idx == prev || idx == curr || idx == next {
					continue
				}
				if roadPointInTriangle(poly[idx], poly[prev], poly[curr], poly[next]) {
					contains = true
					break
				}
			}
			if contains {
				continue
			}
			tris = append(tris, [3]int{prev, curr, next})
			indices = append(indices[:i], indices[i+1:]...)
			earFound = true
			break
		}
		if !earFound {
			break
		}
		guard++
	}
	if len(indices) == 3 {
		tris = append(tris, [3]int{indices[0], indices[1], indices[2]})
	}
	if len(tris) == 0 && len(poly) >= 3 {
		for i := 1; i < len(poly)-1; i++ {
			tris = append(tris, [3]int{0, i, i + 1})
		}
	}
	return tris
}

func roadIsConvex(a, b, c roadPoint, ccw bool) bool {
	cross := (b.X-a.X)*(c.Z-a.Z) - (b.Z-a.Z)*(c.X-a.X)
	if ccw {
		return cross > roadGeomEpsilon
	}
	return cross < -roadGeomEpsilon
}

func roadPointInTriangle(p, a, b, c roadPoint) bool {
	d1 := roadTriangleSign(p, a, b)
	d2 := roadTriangleSign(p, b, c)
	d3 := roadTriangleSign(p, c, a)
	hasNeg := d1 < -roadGeomEpsilon || d2 < -roadGeomEpsilon || d3 < -roadGeomEpsilon
	hasPos := d1 > roadGeomEpsilon || d2 > roadGeomEpsilon || d3 > roadGeomEpsilon
	return !(hasNeg && hasPos)
}

func roadTriangleSign(p1, p2, p3 roadPoint) float32 {
	return (p1.X-p3.X)*(p2.Z-p3.Z) - (p2.X-p3.X)*(p1.Z-p3.Z)
}

func pointInRoadPolygon(p roadPoint, poly []roadPoint) bool {
	if len(poly) < 3 {
		return false
	}
	inside := false
	j := len(poly) - 1
	for i := range poly {
		pi := poly[i]
		pj := poly[j]
		if ((pi.Z > p.Z) != (pj.Z > p.Z)) &&
			(p.X < (pj.X-pi.X)*(p.Z-pi.Z)/(pj.Z-pi.Z)+pi.X) {
			inside = !inside
		}
		j = i
	}
	return inside
}

func nearestRoadCurb(p roadPoint, segments []roadSegment) (roadCurbType, float32) {
	best := float32(math.MaxFloat32)
	curb := roadCurbHard
	for _, segment := range segments {
		d2 := roadPointSegmentDistance2(p, segment.A, segment.B)
		if d2 < best {
			best = d2
			curb = segment.Curb
		}
	}
	return curb, float32(math.Sqrt(float64(best)))
}

func roadDistanceToSegments(p roadPoint, segments []roadSegment) float32 {
	_, d := nearestRoadCurb(p, segments)
	return d
}

func roadPointSegmentDistance2(p, a, b roadPoint) float32 {
	dx := b.X - a.X
	dz := b.Z - a.Z
	l2 := dx*dx + dz*dz
	if l2 <= roadGeomEpsilon {
		return roadPointDistance2(p, a)
	}
	t := ((p.X-a.X)*dx + (p.Z-a.Z)*dz) / l2
	t = clamp32(t, 0, 1)
	proj := roadPoint{X: a.X + dx*t, Z: a.Z + dz*t}
	return roadPointDistance2(p, proj)
}

func cleanRoadPolygon(in []roadPoint) []roadPoint {
	if len(in) == 0 {
		return nil
	}
	out := make([]roadPoint, 0, len(in))
	for _, p := range in {
		if len(out) == 0 || !roadPointsNear(out[len(out)-1], p) {
			out = append(out, p)
		}
	}
	if len(out) > 1 && roadPointsNear(out[0], out[len(out)-1]) {
		out = out[:len(out)-1]
	}
	return out
}

func roadPolygonArea(poly []roadPoint) float32 {
	var area float32
	for i := range poly {
		j := (i + 1) % len(poly)
		area += poly[i].X*poly[j].Z - poly[j].X*poly[i].Z
	}
	return area * 0.5
}

func roadPolygonBounds(poly []roadPoint) roadBounds {
	if len(poly) == 0 {
		return roadBounds{}
	}
	b := roadBounds{MinX: poly[0].X, MaxX: poly[0].X, MinZ: poly[0].Z, MaxZ: poly[0].Z}
	for _, p := range poly[1:] {
		b.MinX = min32(b.MinX, p.X)
		b.MaxX = max32(b.MaxX, p.X)
		b.MinZ = min32(b.MinZ, p.Z)
		b.MaxZ = max32(b.MaxZ, p.Z)
	}
	return b
}

func (b roadBounds) contains(p roadPoint) bool {
	return p.X >= b.MinX-roadGeomEpsilon &&
		p.X <= b.MaxX+roadGeomEpsilon &&
		p.Z >= b.MinZ-roadGeomEpsilon &&
		p.Z <= b.MaxZ+roadGeomEpsilon
}

func (b roadBounds) expanded(amount float32) roadBounds {
	if amount <= 0 {
		return b
	}
	return roadBounds{
		MinX: b.MinX - amount,
		MaxX: b.MaxX + amount,
		MinZ: b.MinZ - amount,
		MaxZ: b.MaxZ + amount,
	}
}

func (b roadBounds) intersects(other roadBounds) bool {
	return !(other.MaxX < b.MinX ||
		other.MinX > b.MaxX ||
		other.MaxZ < b.MinZ ||
		other.MinZ > b.MaxZ)
}

func roadPointsNear(a, b roadPoint) bool {
	return roadPointDistance2(a, b) <= roadGeomEpsilon*roadGeomEpsilon
}

func roadPointDistance(a, b roadPoint) float32 {
	return float32(math.Sqrt(float64(roadPointDistance2(a, b))))
}

func roadPointDistance2(a, b roadPoint) float32 {
	dx := a.X - b.X
	dz := a.Z - b.Z
	return dx*dx + dz*dz
}

func roadDot(a, b roadPoint) float32 {
	return a.X*b.X + a.Z*b.Z
}

func min32(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}
