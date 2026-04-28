package main

import (
	"bytes"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	rl "github.com/gen2brain/raylib-go/raylib"
)

type sceneObjects struct {
	BuildingRegions []buildingRegion
	BuildingCount   int
	TreeFoliage     treeFoliageResources
	Trees           []treeInstance
	streaming       *buildingStreaming
}

type buildingRegion struct {
	Path          string
	Model         streamedBuildingModel
	Position      rl.Vector3
	BuildingCount int
	State         buildingRegionState
}

type buildingRegionMetadata struct {
	OriginEPSG2180 []float64 `json:"origin_epsg2180"`
	Buildings      int
}

type parsedBuildingGLB struct {
	Meshes    []parsedBuildingMesh
	Materials []parsedBuildingMaterial
}

type parsedBuildingMesh struct {
	Vertices      []float32
	Normals       []float32
	Texcoords     []float32
	Indices       []uint16
	MaterialIndex int
}

type parsedBuildingMaterial struct {
	Color        rl.Color
	TextureImage image.Image
}

type streamedBuildingModel struct {
	Meshes       []rl.Mesh
	Materials    []rl.Material
	MeshMaterial []int
	Textures     []rl.Texture2D
}

type buildingRegionUpload struct {
	Data         parsedBuildingGLB
	Model        streamedBuildingModel
	NextMaterial int
	NextMesh     int
}

type treeFoliageResources struct {
	Texture     rl.Texture2D
	Mesh        rl.Mesh
	Material    rl.Material
	Shader      rl.Shader
	ShaderValid bool
	Loaded      bool
}

type treeInstance struct {
	X           float32
	Z           float32
	BaseY       float32
	Height      float32
	TrunkRadius float32
	CrownRadius float32
	IsShrub     bool
}

type visibleTreeDraw struct {
	Tree      treeInstance
	Distance2 float32
}

type treeRenderStyle struct {
	Variant     int
	WidthScale  float32
	HeightScale float32
	LogRatio    float32
	Tint        rl.Color
	OffsetX     float32
	OffsetZ     float32
}

type sceneCPUData struct {
	Regions      []buildingRegion
	Trees        []treeInstance
	FoliageAtlas *image.NRGBA
	Problems     []error
}

func prepareSceneCPU(mapDef *mapDefinition, terrain *terrainData, progress func(string)) *sceneCPUData {
	out := &sceneCPUData{}

	if progress != nil {
		progress("scanning building regions")
	}
	regions, problems := parseBuildingRegionsMetadata(mapDef.BuildingGLBPaths, terrain)
	out.Regions = regions
	out.Problems = append(out.Problems, problems...)

	if progress != nil {
		progress("loading trees")
	}
	for _, path := range mapDef.TreePaths {
		trees, err := loadTreeInstances(path, terrain)
		if err != nil {
			out.Problems = append(out.Problems, fmt.Errorf("%s: %w", filepath.Base(path), err))
			continue
		}
		out.Trees = append(out.Trees, trees...)
	}

	if progress != nil {
		progress("loading shrubs")
	}
	for _, path := range mapDef.ShrubMaskPaths {
		shrubs, err := loadShrubInstances(path, terrain)
		if err != nil {
			out.Problems = append(out.Problems, fmt.Errorf("%s: %w", filepath.Base(path), err))
			continue
		}
		out.Trees = append(out.Trees, shrubs...)
	}

	if len(out.Trees) > 0 {
		if progress != nil {
			progress("generating foliage")
		}
		out.FoliageAtlas = buildFoliageAtlas()
	}
	return out
}

func unloadSceneObjects(objects *sceneObjects) {
	if objects == nil {
		return
	}
	stopBuildingStreaming(objects)
	for i := range objects.BuildingRegions {
		unloadStreamedBuildingModel(objects.BuildingRegions[i].Model)
		objects.BuildingRegions[i].Model = streamedBuildingModel{}
	}
	if objects.TreeFoliage.Loaded {
		rl.UnloadMesh(&objects.TreeFoliage.Mesh)
		// UnloadMaterial unloads both the attached shader and the albedo texture,
		// so do not unload Texture/Shader explicitly here — that's a double-free.
		rl.UnloadMaterial(objects.TreeFoliage.Material)
	}
}

func drawSceneObjects(camera rl.Camera, objects *sceneObjects) {
	if objects == nil {
		return
	}
	for i := range objects.BuildingRegions {
		region := &objects.BuildingRegions[i]
		if region.State != regionStateLoaded {
			continue
		}
		drawStreamedBuildingModel(region.Model, region.Position)
	}

	visibleTrees := visibleTreesForCamera(camera, objects.Trees)
	drawTreeTrunks(visibleTrees)
	drawTreeFoliage(objects.TreeFoliage, visibleTrees)
}

func visibleTreesForCamera(camera rl.Camera, trees []treeInstance) []visibleTreeDraw {
	cameraX := camera.Position.X
	cameraZ := camera.Position.Z
	treeDrawDistance := float32(650)
	if camera.Position.Y > 150 {
		treeDrawDistance += camera.Position.Y * 2.2
	}
	if treeDrawDistance > 1400 {
		treeDrawDistance = 1400
	}
	treeLimit2 := treeDrawDistance * treeDrawDistance

	visibleTrees := make([]visibleTreeDraw, 0, len(trees))
	for _, tree := range trees {
		distance2 := horizontalDistanceSquared(cameraX, cameraZ, tree.X, tree.Z)
		if distance2 > treeLimit2 {
			continue
		}
		visibleTrees = append(visibleTrees, visibleTreeDraw{
			Tree:      tree,
			Distance2: distance2,
		})
	}

	return visibleTrees
}

func drawTreeTrunks(visibleTrees []visibleTreeDraw) {
	trunkColor := rl.NewColor(103, 76, 46, 255)
	for _, visible := range visibleTrees {
		tree := visible.Tree
		style := treeRenderStyleFor(tree)

		trunkHeight := tree.Height * style.LogRatio
		if !tree.IsShrub && trunkHeight < 1.4 {
			trunkHeight = 1.4
		}

		crownHeight := tree.Height * 0.78 * style.HeightScale
		if !tree.IsShrub && crownHeight < 2.2 {
			crownHeight = 2.2
		}
		trunkPos := rl.NewVector3(tree.X, tree.BaseY, tree.Z)
		rl.DrawCylinder(trunkPos, tree.TrunkRadius*0.8, tree.TrunkRadius, trunkHeight+crownHeight*0.20, 6, trunkColor)
	}
}

func drawTreeFoliage(foliage treeFoliageResources, visibleTrees []visibleTreeDraw) {
	if !foliage.Loaded {
		for _, visible := range visibleTrees {
			tree := visible.Tree
			style := treeRenderStyleFor(tree)
			trunkHeight := tree.Height * style.LogRatio
			if trunkHeight < 1.4 {
				trunkHeight = 1.4
			}
			crownBaseY := tree.BaseY + trunkHeight*0.82
			crownHeight := tree.BaseY + tree.Height - crownBaseY
			if crownHeight < 1 {
				crownHeight = 1
			}

			crownPos := rl.NewVector3(tree.X, crownBaseY, tree.Z)
			rl.DrawCylinder(crownPos, tree.CrownRadius*0.12, tree.CrownRadius, crownHeight, 7, treeCrownColor(tree))
		}
		return
	}

	rl.DisableBackfaceCulling()
	rl.DrawMesh(foliage.Mesh, foliage.Material, rl.MatrixIdentity())
	rl.EnableBackfaceCulling()
}

const foliageSpriteSize = 128
const foliageVariantCount = 10

func buildFoliageAtlas() *image.NRGBA {
	atlas := image.NewNRGBA(image.Rect(0, 0, foliageSpriteSize*foliageVariantCount, foliageSpriteSize))
	for variant := 0; variant < foliageVariantCount; variant++ {
		drawGeneratedFoliageSprite(atlas, foliageSpriteSize, variant)
	}
	return atlas
}

func uploadTreeFoliage(atlas *image.NRGBA, trees []treeInstance) treeFoliageResources {
	imageData := goImageToRaylibImage(atlas)
	texture := rl.LoadTextureFromImage(imageData)
	if texture.ID == 0 {
		return treeFoliageResources{}
	}
	rl.GenTextureMipmaps(&texture)
	rl.SetTextureFilter(texture, rl.FilterTrilinear)
	rl.SetTextureWrap(texture, rl.WrapClamp)

	mesh := buildTreeFoliageMesh(trees, foliageVariantCount)
	if mesh.VertexCount == 0 {
		rl.UnloadTexture(texture)
		return treeFoliageResources{}
	}
	material := rl.LoadMaterialDefault()
	rl.SetMaterialTexture(&material, rl.MapAlbedo, texture)

	shader := rl.LoadShaderFromMemory("", foliageFragmentShader)
	shaderValid := shader.ID != 0
	if shaderValid {
		material.Shader = shader
	}

	return treeFoliageResources{
		Texture:     texture,
		Mesh:        mesh,
		Material:    material,
		Shader:      shader,
		ShaderValid: shaderValid,
		Loaded:      true,
	}
}

// goImageToRaylibImage builds an rl.Image backed by a contiguous RGBA8 buffer.
// raylib-go's NewImageFromImage performs one CGO call per pixel, which made
// uploading building textures dominate map load time.
func goImageToRaylibImage(img image.Image) *rl.Image {
	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	buf := make([]byte, w*h*4)

	switch src := img.(type) {
	case *image.NRGBA:
		if src.Stride == w*4 && bounds.Min.X == 0 && bounds.Min.Y == 0 {
			copy(buf, src.Pix)
		} else {
			for y := 0; y < h; y++ {
				srcRow := src.Pix[(y+bounds.Min.Y-src.Rect.Min.Y)*src.Stride+(bounds.Min.X-src.Rect.Min.X)*4:]
				copy(buf[y*w*4:(y+1)*w*4], srcRow[:w*4])
			}
		}
	case *image.RGBA:
		// image.RGBA is premultiplied; un-premultiply so the GPU sees straight RGBA.
		for y := 0; y < h; y++ {
			srcRow := src.Pix[(y+bounds.Min.Y-src.Rect.Min.Y)*src.Stride+(bounds.Min.X-src.Rect.Min.X)*4:]
			dstRow := buf[y*w*4 : (y+1)*w*4]
			for x := 0; x < w; x++ {
				r := srcRow[x*4]
				g := srcRow[x*4+1]
				b := srcRow[x*4+2]
				a := srcRow[x*4+3]
				if a != 0 && a != 255 {
					r = uint8(uint32(r) * 255 / uint32(a))
					g = uint8(uint32(g) * 255 / uint32(a))
					b = uint8(uint32(b) * 255 / uint32(a))
				}
				dstRow[x*4] = r
				dstRow[x*4+1] = g
				dstRow[x*4+2] = b
				dstRow[x*4+3] = a
			}
		}
	default:
		i := 0
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				r, g, b, a := img.At(x, y).RGBA()
				if a != 0 && a != 0xffff {
					r = r * 0xffff / a
					g = g * 0xffff / a
					b = b * 0xffff / a
				}
				buf[i] = uint8(r >> 8)
				buf[i+1] = uint8(g >> 8)
				buf[i+2] = uint8(b >> 8)
				buf[i+3] = uint8(a >> 8)
				i += 4
			}
		}
	}

	return rl.NewImage(buf, int32(w), int32(h), 1, rl.UncompressedR8g8b8a8)
}

// foliageFragmentShader discards near-transparent fragments so foliage quads
// write depth only where actual leaf pixels are drawn. Without this, the
// transparent region of a closer tree's quad writes depth across its full
// rectangle and occludes trees behind it, leaving a visible leaf-box hole.
const foliageFragmentShader = `#version 330
in vec2 fragTexCoord;
in vec4 fragColor;
uniform sampler2D texture0;
uniform vec4 colDiffuse;
out vec4 finalColor;
void main() {
    vec4 texelColor = texture(texture0, fragTexCoord);
    vec4 col = texelColor * colDiffuse * fragColor;
    if (col.a < 0.45) discard;
    finalColor = vec4(col.rgb, 1.0);
}
`

type foliageBlob struct {
	X  float64
	Y  float64
	RX float64
	RY float64
}

func drawGeneratedFoliageSprite(atlas *image.NRGBA, spriteSize int, variant int) {
	rng := rand.New(rand.NewSource(int64(71237 + variant*19087)))
	blobCount := 12 + rng.Intn(6)
	blobs := make([]foliageBlob, 0, blobCount)
	for i := 0; i < blobCount; i++ {
		blobs = append(blobs, foliageBlob{
			X:  rng.Float64()*1.18 - 0.59,
			Y:  rng.Float64()*1.12 - 0.64,
			RX: 0.26 + rng.Float64()*0.28,
			RY: 0.24 + rng.Float64()*0.33,
		})
	}

	baseR := uint8(38 + rng.Intn(22))
	baseG := uint8(92 + rng.Intn(48))
	baseB := uint8(42 + rng.Intn(20))
	if variant%4 == 2 {
		baseR += 14
		baseG += 12
		baseB += 4
	}
	if variant%5 == 3 {
		baseR += 8
		baseG -= 15
	}

	tileX := variant * spriteSize
	for py := 0; py < spriteSize; py++ {
		ny := (float64(py)+0.5)/float64(spriteSize)*2 - 1
		for px := 0; px < spriteSize; px++ {
			nx := (float64(px)+0.5)/float64(spriteSize)*2 - 1

			field := (1 - (nx*nx)/(0.74*0.74) - ((ny+0.04)*(ny+0.04))/(0.93*0.93)) * 0.58
			for _, blob := range blobs {
				dx := (nx - blob.X) / blob.RX
				dy := (ny - blob.Y) / blob.RY
				field = math.Max(field, 1-dx*dx-dy*dy)
			}

			leafNoise := foliageNoise01(px, py, variant) - 0.5
			clusterNoise := foliageNoiseSmooth(float64(px)/2.5, float64(py)/2.5, variant+113)
			alphaValue := field + leafNoise*0.42
			if clusterNoise < 0.25 {
				continue
			}
			if alphaValue <= 0.02 {
				continue
			}

			alpha := uint8(clampFloat64(alphaValue*260, 0, 230))
			if alpha < 34 {
				continue
			}

			shade := 0.82 + (1-(ny+1)*0.5)*0.22 + (foliageNoise01(px, py, variant+67)-0.5)*0.24
			edgeDarken := clampFloat64(field, 0.2, 1)
			shade *= 0.78 + edgeDarken*0.24
			col := color.NRGBA{
				R: uint8(clampFloat64(float64(baseR)*shade, 0, 255)),
				G: uint8(clampFloat64(float64(baseG)*shade, 0, 255)),
				B: uint8(clampFloat64(float64(baseB)*shade, 0, 255)),
				A: alpha,
			}
			atlas.SetNRGBA(tileX+px, py, col)
		}
	}
}

func buildTreeFoliageMesh(trees []treeInstance, variantCount int) rl.Mesh {
	var vertices []float32
	var texcoords []float32
	var colors []uint8

	for _, tree := range trees {
		style := treeRenderStyleFor(tree)
		trunkHeight := tree.Height * style.LogRatio
		if !tree.IsShrub && trunkHeight < 1.4 {
			trunkHeight = 1.4
		}

		crownHeight := tree.Height * 0.78 * style.HeightScale
		if !tree.IsShrub && crownHeight < 2.2 {
			crownHeight = 2.2
		}
		crownWidth := max32(tree.CrownRadius*2.9, tree.Height*0.48) * style.WidthScale
		center := rl.NewVector3(
			tree.X+style.OffsetX,
			tree.BaseY+trunkHeight+crownHeight*0.38,
			tree.Z+style.OffsetZ,
		)

		baseAngle := seededUnit(treeSeed(tree), 41) * math.Pi
		appendFoliageQuad(&vertices, &texcoords, &colors, center, crownWidth, crownHeight, baseAngle, style.Variant, variantCount, style.Tint)

		layerTint := style.Tint
		layerTint.A = uint8(float32(layerTint.A) * 0.86)
		appendFoliageQuad(&vertices, &texcoords, &colors, center, crownWidth*0.9, crownHeight*0.94, baseAngle+math.Pi*0.5, style.Variant+3, variantCount, layerTint)

		if tree.Height > 9 {
			topTint := style.Tint
			topTint.A = uint8(float32(topTint.A) * 0.68)
			topCenter := rl.NewVector3(center.X, center.Y+crownHeight*0.04, center.Z)
			appendFoliageQuad(&vertices, &texcoords, &colors, topCenter, crownWidth*0.72, crownHeight*0.78, baseAngle+math.Pi*0.25, style.Variant+6, variantCount, topTint)
		}
	}

	if len(vertices) == 0 {
		return rl.Mesh{}
	}

	mesh := rl.Mesh{
		VertexCount:   int32(len(vertices) / 3),
		TriangleCount: int32(len(vertices) / 9),
		Vertices:      &vertices[0],
		Texcoords:     &texcoords[0],
		Colors:        &colors[0],
	}
	rl.UploadMesh(&mesh, false)
	return mesh
}

func appendFoliageQuad(vertices, texcoords *[]float32, colors *[]uint8, center rl.Vector3, width, height float32, angle float32, variant int, variantCount int, tint rl.Color) {
	variant %= variantCount
	if variant < 0 {
		variant += variantCount
	}

	u0 := float32(variant) / float32(variantCount)
	u1 := float32(variant+1) / float32(variantCount)
	const v0 = float32(0)
	const v1 = float32(1)

	halfWidth := width * 0.5
	halfHeight := height * 0.5
	rightX := float32(math.Cos(float64(angle))) * halfWidth
	rightZ := float32(math.Sin(float64(angle))) * halfWidth

	bottomLeft := rl.NewVector3(center.X-rightX, center.Y-halfHeight, center.Z-rightZ)
	topLeft := rl.NewVector3(center.X-rightX, center.Y+halfHeight, center.Z-rightZ)
	topRight := rl.NewVector3(center.X+rightX, center.Y+halfHeight, center.Z+rightZ)
	bottomRight := rl.NewVector3(center.X+rightX, center.Y-halfHeight, center.Z+rightZ)

	appendFoliageVertex(vertices, texcoords, colors, bottomLeft, u0, v1, tint)
	appendFoliageVertex(vertices, texcoords, colors, topLeft, u0, v0, tint)
	appendFoliageVertex(vertices, texcoords, colors, topRight, u1, v0, tint)

	appendFoliageVertex(vertices, texcoords, colors, bottomLeft, u0, v1, tint)
	appendFoliageVertex(vertices, texcoords, colors, topRight, u1, v0, tint)
	appendFoliageVertex(vertices, texcoords, colors, bottomRight, u1, v1, tint)
}

func appendFoliageVertex(vertices, texcoords *[]float32, colors *[]uint8, position rl.Vector3, u, v float32, tint rl.Color) {
	*vertices = append(*vertices, position.X, position.Y, position.Z)
	*texcoords = append(*texcoords, u, v)
	*colors = append(*colors, tint.R, tint.G, tint.B, tint.A)
}

func treeRenderStyleFor(tree treeInstance) treeRenderStyle {
	seed := treeSeed(tree)
	if tree.IsShrub {
		return treeRenderStyle{
			Variant:     int(seed % 10),
			WidthScale:  0.8 + seededUnit(seed, 5)*0.6,
			HeightScale: 0.5 + seededUnit(seed, 9)*1.0,           // Semi-randomised height scale
			LogRatio:    0.0 + float32(seededUnit(seed, 45)*0.18), // Semi-randomised leaf-to-log ratio (still low)
			Tint: rl.NewColor(
				uint8(180+seededUnit(seed, 13)*30),
				uint8(200+seededUnit(seed, 17)*30),
				uint8(150+seededUnit(seed, 21)*40),
				uint8(210+seededUnit(seed, 25)*34),
			),
			OffsetX: (seededUnit(seed, 33) - 0.5) * tree.CrownRadius * 0.5,
			OffsetZ: (seededUnit(seed, 37) - 0.5) * tree.CrownRadius * 0.5,
		}
	}

	return treeRenderStyle{
		Variant:     int(seed % 10),
		WidthScale:  1.3 + seededUnit(seed, 5)*0.4,
		HeightScale: 1.35 + seededUnit(seed, 9)*0.3,
		LogRatio:    0.15 + float32(seededUnit(seed, 45)*0.10),
		Tint: rl.NewColor(
			uint8(226+seededUnit(seed, 13)*24),
			uint8(232+seededUnit(seed, 17)*20),
			uint8(222+seededUnit(seed, 21)*22),
			uint8(210+seededUnit(seed, 25)*34),
		),
		OffsetX: (seededUnit(seed, 33) - 0.5) * tree.CrownRadius * 0.25,
		OffsetZ: (seededUnit(seed, 37) - 0.5) * tree.CrownRadius * 0.25,
	}
}

func treeSeed(tree treeInstance) uint32 {
	x := uint32(int32(math.Round(float64(tree.X * 10))))
	z := uint32(int32(math.Round(float64(tree.Z * 10))))
	h := uint32(int32(math.Round(float64(tree.Height * 100))))
	return mixUint32(x*0x9e3779b1 ^ z*0x85ebca6b ^ h*0xc2b2ae35)
}

func seededUnit(seed uint32, salt uint32) float32 {
	return float32(mixUint32(seed^salt*0x27d4eb2d)&0xffff) / 65535
}

func foliageNoise01(x, y int, seed int) float64 {
	value := mixUint32(uint32(x)*0x8da6b343 ^ uint32(y)*0xd8163841 ^ uint32(seed)*0xcb1ab31f)
	return float64(value&0xffffff) / float64(0xffffff)
}

// foliageNoiseSmooth samples foliageNoise01 at fractional coordinates using
// bilinear interpolation with smoothstep weights for continuous, non-pixelated output.
func foliageNoiseSmooth(x, y float64, seed int) float64 {
	ix := int(math.Floor(x))
	iy := int(math.Floor(y))
	fx := x - math.Floor(x)
	fy := y - math.Floor(y)
	ux := fx * fx * (3 - 2*fx)
	uy := fy * fy * (3 - 2*fy)
	v00 := foliageNoise01(ix, iy, seed)
	v10 := foliageNoise01(ix+1, iy, seed)
	v01 := foliageNoise01(ix, iy+1, seed)
	v11 := foliageNoise01(ix+1, iy+1, seed)
	return v00*(1-ux)*(1-uy) + v10*ux*(1-uy) + v01*(1-ux)*uy + v11*ux*uy
}

func mixUint32(value uint32) uint32 {
	value ^= value >> 16
	value *= 0x7feb352d
	value ^= value >> 15
	value *= 0x846ca68b
	value ^= value >> 16
	return value
}

// parseBuildingRegionsMetadata reads only the JSON chunk of each GLB so we
// learn its origin and building count without paying for vertex/texture
// decoding. The actual mesh+material data is parsed lazily by the streaming
// worker once the camera comes within range.
func parseBuildingRegionsMetadata(glbPaths []string, terrain *terrainData) ([]buildingRegion, []error) {
	regions := make([]buildingRegion, 0, len(glbPaths))
	var problems []error
	for _, path := range glbPaths {
		meta, err := parseBuildingGLBMetadataOnly(path)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", filepath.Base(path), err))
			continue
		}
		regions = append(regions, buildingRegion{
			Path:          path,
			Position:      buildingRegionPosition(terrain, meta.OriginEPSG2180),
			BuildingCount: meta.Buildings,
			State:         regionStateUnloaded,
		})
	}
	return regions, problems
}

func parseBuildingGLBMetadataOnly(path string) (buildingRegionMetadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return buildingRegionMetadata{}, fmt.Errorf("open GLB: %w", err)
	}
	defer f.Close()

	var hdr [12]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return buildingRegionMetadata{}, fmt.Errorf("read GLB header: %w", err)
	}
	if string(hdr[0:4]) != "glTF" {
		return buildingRegionMetadata{}, errors.New("unsupported GLB magic")
	}
	if binary.LittleEndian.Uint32(hdr[4:8]) != 2 {
		return buildingRegionMetadata{}, errors.New("unsupported GLB version")
	}

	var chunkHdr [8]byte
	if _, err := io.ReadFull(f, chunkHdr[:]); err != nil {
		return buildingRegionMetadata{}, fmt.Errorf("read JSON chunk header: %w", err)
	}
	jsonLen := binary.LittleEndian.Uint32(chunkHdr[0:4])
	if binary.LittleEndian.Uint32(chunkHdr[4:8]) != 0x4e4f534a {
		return buildingRegionMetadata{}, errors.New("first GLB chunk is not JSON")
	}

	jsonBytes := make([]byte, jsonLen)
	if _, err := io.ReadFull(f, jsonBytes); err != nil {
		return buildingRegionMetadata{}, fmt.Errorf("read JSON: %w", err)
	}

	var doc struct {
		Extras struct {
			OriginEPSG2180 []float64 `json:"origin_epsg2180"`
			Stats          struct {
				Buildings int `json:"buildings"`
			} `json:"stats"`
		} `json:"extras"`
	}
	if err := json.Unmarshal(jsonBytes, &doc); err != nil {
		return buildingRegionMetadata{}, fmt.Errorf("decode JSON: %w", err)
	}
	if len(doc.Extras.OriginEPSG2180) < 3 {
		return buildingRegionMetadata{}, errors.New("missing origin_epsg2180")
	}
	return buildingRegionMetadata{
		OriginEPSG2180: doc.Extras.OriginEPSG2180,
		Buildings:      doc.Extras.Stats.Buildings,
	}, nil
}

func parseBuildingGLBWithMetadata(path string) (parsedBuildingGLB, buildingRegionMetadata, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return parsedBuildingGLB{}, buildingRegionMetadata{}, fmt.Errorf("read GLB: %w", err)
	}
	doc, bin, err := parseGLBChunks(raw)
	if err != nil {
		return parsedBuildingGLB{}, buildingRegionMetadata{}, err
	}

	if len(doc.Extras.OriginEPSG2180) < 3 {
		return parsedBuildingGLB{}, buildingRegionMetadata{}, errors.New("missing origin_epsg2180")
	}
	metadata := buildingRegionMetadata{
		OriginEPSG2180: doc.Extras.OriginEPSG2180,
		Buildings:      doc.Extras.Stats.Buildings,
	}

	parsed, err := buildParsedGLB(doc, bin)
	if err != nil {
		return parsedBuildingGLB{}, buildingRegionMetadata{}, err
	}
	return parsed, metadata, nil
}

func buildParsedGLB(doc buildingGLBDocument, bin []byte) (parsedBuildingGLB, error) {

	materials, err := parseBuildingMaterials(doc, bin)
	if err != nil {
		return parsedBuildingGLB{}, err
	}
	if len(materials) == 0 {
		materials = []parsedBuildingMaterial{{Color: rl.White}}
	}

	var meshes []parsedBuildingMesh
	for _, mesh := range doc.Meshes {
		for _, primitive := range mesh.Primitives {
			positionAccessor, ok := primitive.Attributes["POSITION"]
			if !ok {
				continue
			}

			vertices, err := readAccessorFloat32(doc, bin, positionAccessor, "VEC3")
			if err != nil {
				return parsedBuildingGLB{}, fmt.Errorf("positions: %w", err)
			}

			var normals []float32
			if normalAccessor, ok := primitive.Attributes["NORMAL"]; ok {
				normals, err = readAccessorFloat32(doc, bin, normalAccessor, "VEC3")
				if err != nil {
					return parsedBuildingGLB{}, fmt.Errorf("normals: %w", err)
				}
			}

			var texcoords []float32
			if texAccessor, ok := primitive.Attributes["TEXCOORD_0"]; ok {
				texcoords, err = readAccessorFloat32(doc, bin, texAccessor, "VEC2")
				if err != nil {
					return parsedBuildingGLB{}, fmt.Errorf("texcoords: %w", err)
				}
			}

			indices, err := readAccessorIndices(doc, bin, primitive.Indices)
			if err != nil {
				return parsedBuildingGLB{}, fmt.Errorf("indices: %w", err)
			}

			materialIndex := primitive.Material
			if materialIndex < 0 || materialIndex >= len(materials) {
				materialIndex = 0
			}
			meshes = append(meshes, parsedBuildingMesh{
				Vertices:      vertices,
				Normals:       normals,
				Texcoords:     texcoords,
				Indices:       indices,
				MaterialIndex: materialIndex,
			})
		}
	}
	if len(meshes) == 0 {
		return parsedBuildingGLB{}, errors.New("no triangle meshes in GLB")
	}

	return parsedBuildingGLB{Meshes: meshes, Materials: materials}, nil
}

type buildingGLBDocument struct {
	Extras struct {
		OriginEPSG2180 []float64 `json:"origin_epsg2180"`
		Stats          struct {
			Buildings int `json:"buildings"`
		} `json:"stats"`
	} `json:"extras"`
	BufferViews []struct {
		Buffer     int `json:"buffer"`
		ByteOffset int `json:"byteOffset"`
		ByteLength int `json:"byteLength"`
		ByteStride int `json:"byteStride"`
	} `json:"bufferViews"`
	Accessors []struct {
		BufferView    int       `json:"bufferView"`
		ByteOffset    int       `json:"byteOffset"`
		ComponentType int       `json:"componentType"`
		Count         int       `json:"count"`
		Type          string    `json:"type"`
		Min           []float64 `json:"min"`
		Max           []float64 `json:"max"`
	} `json:"accessors"`
	Meshes []struct {
		Primitives []struct {
			Attributes map[string]int `json:"attributes"`
			Indices    int            `json:"indices"`
			Material   int            `json:"material"`
		} `json:"primitives"`
	} `json:"meshes"`
	Materials []struct {
		PBRMetallicRoughness struct {
			BaseColorFactor  []float64 `json:"baseColorFactor"`
			BaseColorTexture *struct {
				Index int `json:"index"`
			} `json:"baseColorTexture"`
		} `json:"pbrMetallicRoughness"`
	} `json:"materials"`
	Images []struct {
		BufferView int    `json:"bufferView"`
		MimeType   string `json:"mimeType"`
	} `json:"images"`
	Textures []struct {
		Source int `json:"source"`
	} `json:"textures"`
}

func parseGLBChunks(raw []byte) (buildingGLBDocument, []byte, error) {
	if len(raw) < 20 {
		return buildingGLBDocument{}, nil, errors.New("GLB too small")
	}
	if string(raw[0:4]) != "glTF" {
		return buildingGLBDocument{}, nil, errors.New("unsupported GLB magic")
	}
	version := binary.LittleEndian.Uint32(raw[4:8])
	if version != 2 {
		return buildingGLBDocument{}, nil, errors.New("unsupported GLB version")
	}
	totalLength := int(binary.LittleEndian.Uint32(raw[8:12]))
	if totalLength > len(raw) {
		return buildingGLBDocument{}, nil, errors.New("truncated GLB")
	}

	offset := 12
	if offset+8 > totalLength {
		return buildingGLBDocument{}, nil, errors.New("missing GLB JSON chunk")
	}
	jsonLength := int(binary.LittleEndian.Uint32(raw[offset : offset+4]))
	jsonType := binary.LittleEndian.Uint32(raw[offset+4 : offset+8])
	offset += 8
	if jsonType != 0x4e4f534a {
		return buildingGLBDocument{}, nil, errors.New("first GLB chunk is not JSON")
	}
	if offset+jsonLength > totalLength {
		return buildingGLBDocument{}, nil, errors.New("truncated GLB JSON chunk")
	}
	var doc buildingGLBDocument
	if err := json.NewDecoder(bytes.NewReader(raw[offset : offset+jsonLength])).Decode(&doc); err != nil {
		return buildingGLBDocument{}, nil, fmt.Errorf("decode GLB JSON: %w", err)
	}
	offset += jsonLength

	if offset+8 > totalLength {
		return buildingGLBDocument{}, nil, errors.New("missing GLB BIN chunk")
	}
	binLength := int(binary.LittleEndian.Uint32(raw[offset : offset+4]))
	binType := binary.LittleEndian.Uint32(raw[offset+4 : offset+8])
	offset += 8
	if binType != 0x004e4942 {
		return buildingGLBDocument{}, nil, errors.New("second GLB chunk is not BIN")
	}
	if offset+binLength > totalLength {
		return buildingGLBDocument{}, nil, errors.New("truncated GLB BIN chunk")
	}
	return doc, raw[offset : offset+binLength], nil
}

func parseBuildingMaterials(doc buildingGLBDocument, bin []byte) ([]parsedBuildingMaterial, error) {
	materials := make([]parsedBuildingMaterial, len(doc.Materials))
	for i, material := range doc.Materials {
		materials[i].Color = rl.White
		if factor := material.PBRMetallicRoughness.BaseColorFactor; len(factor) >= 4 {
			materials[i].Color = rl.NewColor(
				uint8(clampFloat64(factor[0]*255, 0, 255)),
				uint8(clampFloat64(factor[1]*255, 0, 255)),
				uint8(clampFloat64(factor[2]*255, 0, 255)),
				uint8(clampFloat64(factor[3]*255, 0, 255)),
			)
		}
		if material.PBRMetallicRoughness.BaseColorTexture == nil {
			continue
		}
		textureIndex := material.PBRMetallicRoughness.BaseColorTexture.Index
		if textureIndex < 0 || textureIndex >= len(doc.Textures) {
			continue
		}
		imageIndex := doc.Textures[textureIndex].Source
		if imageIndex < 0 || imageIndex >= len(doc.Images) {
			continue
		}
		imageDef := doc.Images[imageIndex]
		if imageDef.BufferView < 0 || imageDef.BufferView >= len(doc.BufferViews) {
			continue
		}
		imageBytes, err := bufferViewBytes(bin, doc.BufferViews[imageDef.BufferView])
		if err != nil {
			return nil, err
		}
		img, _, err := image.Decode(bytes.NewReader(imageBytes))
		if err != nil {
			return nil, fmt.Errorf("decode material image: %w", err)
		}
		materials[i].TextureImage = img
	}
	return materials, nil
}

func readAccessorFloat32(doc buildingGLBDocument, bin []byte, accessorIndex int, expectedType string) ([]float32, error) {
	if accessorIndex < 0 || accessorIndex >= len(doc.Accessors) {
		return nil, errors.New("accessor index out of range")
	}
	accessor := doc.Accessors[accessorIndex]
	if accessor.ComponentType != 5126 {
		return nil, fmt.Errorf("unsupported float accessor component type %d", accessor.ComponentType)
	}
	if accessor.Type != expectedType {
		return nil, fmt.Errorf("expected accessor type %s, got %s", expectedType, accessor.Type)
	}
	components := gltfAccessorComponents(accessor.Type)
	if components == 0 {
		return nil, fmt.Errorf("unsupported accessor type %s", accessor.Type)
	}
	view, err := accessorBufferView(doc, accessor.BufferView)
	if err != nil {
		return nil, err
	}
	stride := view.ByteStride
	if stride == 0 {
		stride = components * 4
	}
	start := view.ByteOffset + accessor.ByteOffset
	values := make([]float32, accessor.Count*components)
	for i := 0; i < accessor.Count; i++ {
		row := start + i*stride
		for c := 0; c < components; c++ {
			off := row + c*4
			if off+4 > len(bin) {
				return nil, errors.New("float accessor exceeds BIN chunk")
			}
			values[i*components+c] = math.Float32frombits(binary.LittleEndian.Uint32(bin[off : off+4]))
		}
	}
	return values, nil
}

func readAccessorIndices(doc buildingGLBDocument, bin []byte, accessorIndex int) ([]uint16, error) {
	if accessorIndex < 0 || accessorIndex >= len(doc.Accessors) {
		return nil, errors.New("index accessor out of range")
	}
	accessor := doc.Accessors[accessorIndex]
	if accessor.Type != "SCALAR" {
		return nil, fmt.Errorf("expected SCALAR index accessor, got %s", accessor.Type)
	}
	view, err := accessorBufferView(doc, accessor.BufferView)
	if err != nil {
		return nil, err
	}
	componentSize := gltfComponentSize(accessor.ComponentType)
	if componentSize == 0 {
		return nil, fmt.Errorf("unsupported index component type %d", accessor.ComponentType)
	}
	stride := view.ByteStride
	if stride == 0 {
		stride = componentSize
	}
	start := view.ByteOffset + accessor.ByteOffset
	indices := make([]uint16, accessor.Count)
	for i := 0; i < accessor.Count; i++ {
		off := start + i*stride
		if off+componentSize > len(bin) {
			return nil, errors.New("index accessor exceeds BIN chunk")
		}
		var value uint32
		switch accessor.ComponentType {
		case 5121:
			value = uint32(bin[off])
		case 5123:
			value = uint32(binary.LittleEndian.Uint16(bin[off : off+2]))
		case 5125:
			value = binary.LittleEndian.Uint32(bin[off : off+4])
		default:
			return nil, fmt.Errorf("unsupported index component type %d", accessor.ComponentType)
		}
		if value > math.MaxUint16 {
			return nil, fmt.Errorf("index %d exceeds uint16 mesh limit", value)
		}
		indices[i] = uint16(value)
	}
	return indices, nil
}

func accessorBufferView(doc buildingGLBDocument, index int) (struct {
	Buffer     int `json:"buffer"`
	ByteOffset int `json:"byteOffset"`
	ByteLength int `json:"byteLength"`
	ByteStride int `json:"byteStride"`
}, error) {
	if index < 0 || index >= len(doc.BufferViews) {
		return struct {
			Buffer     int `json:"buffer"`
			ByteOffset int `json:"byteOffset"`
			ByteLength int `json:"byteLength"`
			ByteStride int `json:"byteStride"`
		}{}, errors.New("buffer view index out of range")
	}
	return doc.BufferViews[index], nil
}

func bufferViewBytes(bin []byte, view struct {
	Buffer     int `json:"buffer"`
	ByteOffset int `json:"byteOffset"`
	ByteLength int `json:"byteLength"`
	ByteStride int `json:"byteStride"`
}) ([]byte, error) {
	if view.ByteOffset < 0 || view.ByteLength < 0 || view.ByteOffset+view.ByteLength > len(bin) {
		return nil, errors.New("buffer view exceeds BIN chunk")
	}
	return bin[view.ByteOffset : view.ByteOffset+view.ByteLength], nil
}

func gltfAccessorComponents(accessorType string) int {
	switch accessorType {
	case "SCALAR":
		return 1
	case "VEC2":
		return 2
	case "VEC3":
		return 3
	case "VEC4":
		return 4
	default:
		return 0
	}
}

func gltfComponentSize(componentType int) int {
	switch componentType {
	case 5120, 5121:
		return 1
	case 5122, 5123:
		return 2
	case 5125, 5126:
		return 4
	default:
		return 0
	}
}

func newBuildingRegionUpload(data parsedBuildingGLB) *buildingRegionUpload {
	return &buildingRegionUpload{
		Data: data,
		Model: streamedBuildingModel{
			Materials:    make([]rl.Material, len(data.Materials)),
			MeshMaterial: make([]int, len(data.Meshes)),
		},
	}
}

func advanceBuildingRegionUpload(upload *buildingRegionUpload) (bool, error) {
	if upload == nil {
		return true, nil
	}
	if upload.NextMaterial < len(upload.Data.Materials) {
		material := upload.Data.Materials[upload.NextMaterial]
		rlMaterial := rl.LoadMaterialDefault()
		rlMaterial.GetMap(int32(rl.MapAlbedo)).Color = material.Color
		if material.TextureImage != nil {
			imageData := goImageToRaylibImage(material.TextureImage)
			texture := rl.LoadTextureFromImage(imageData)
			if texture.ID != 0 {
				rl.GenTextureMipmaps(&texture)
				rl.SetTextureFilter(texture, rl.FilterAnisotropic16x)
				rl.SetTextureWrap(texture, rl.WrapRepeat)
				rl.SetMaterialTexture(&rlMaterial, rl.MapAlbedo, texture)
				upload.Model.Textures = append(upload.Model.Textures, texture)
			}
		}
		upload.Model.Materials[upload.NextMaterial] = rlMaterial
		upload.Data.Materials[upload.NextMaterial].TextureImage = nil
		upload.NextMaterial++
		return uploadDone(upload), nil
	}

	if upload.Model.Meshes == nil {
		upload.Model.Meshes = make([]rl.Mesh, len(upload.Data.Meshes))
	}
	if upload.NextMesh < len(upload.Data.Meshes) {
		parsedMesh := upload.Data.Meshes[upload.NextMesh]
		if len(parsedMesh.Vertices) == 0 || len(parsedMesh.Indices) == 0 {
			return false, errors.New("empty mesh")
		}
		mesh := rl.Mesh{
			VertexCount:   int32(len(parsedMesh.Vertices) / 3),
			TriangleCount: int32(len(parsedMesh.Indices) / 3),
			Vertices:      &parsedMesh.Vertices[0],
			Indices:       &parsedMesh.Indices[0],
		}
		if len(parsedMesh.Normals) > 0 {
			mesh.Normals = &parsedMesh.Normals[0]
		}
		if len(parsedMesh.Texcoords) > 0 {
			mesh.Texcoords = &parsedMesh.Texcoords[0]
		}
		rl.UploadMesh(&mesh, false)
		mesh.Vertices = nil
		mesh.Normals = nil
		mesh.Texcoords = nil
		mesh.Indices = nil
		upload.Model.Meshes[upload.NextMesh] = mesh
		upload.Model.MeshMaterial[upload.NextMesh] = parsedMesh.MaterialIndex
		upload.Data.Meshes[upload.NextMesh].Vertices = nil
		upload.Data.Meshes[upload.NextMesh].Normals = nil
		upload.Data.Meshes[upload.NextMesh].Texcoords = nil
		upload.Data.Meshes[upload.NextMesh].Indices = nil
		upload.NextMesh++
		return uploadDone(upload), nil
	}

	return true, nil
}

func uploadDone(upload *buildingRegionUpload) bool {
	return upload.NextMaterial >= len(upload.Data.Materials) && upload.NextMesh >= len(upload.Data.Meshes)
}

func drawStreamedBuildingModel(model streamedBuildingModel, position rl.Vector3) {
	if len(model.Materials) == 0 {
		return
	}
	transform := rl.MatrixTranslate(position.X, position.Y, position.Z)
	for i, mesh := range model.Meshes {
		materialIndex := 0
		if i < len(model.MeshMaterial) {
			materialIndex = model.MeshMaterial[i]
		}
		if materialIndex < 0 || materialIndex >= len(model.Materials) {
			materialIndex = 0
		}
		rl.DrawMesh(mesh, model.Materials[materialIndex], transform)
	}
}

func unloadStreamedBuildingModel(model streamedBuildingModel) {
	for i := range model.Meshes {
		if model.Meshes[i].VaoID != 0 || model.Meshes[i].VboID != nil {
			rl.UnloadMesh(&model.Meshes[i])
		}
	}
	for _, material := range model.Materials {
		if material.Maps != nil {
			// UnloadMaterial also unloads the textures attached via SetMaterialTexture,
			// so the model.Textures list must not be unloaded again separately.
			rl.UnloadMaterial(material)
		}
	}
}

func buildingRegionPosition(terrain *terrainData, origin []float64) rl.Vector3 {
	localX, localZ := terrainLocalXZ(terrain, origin[0], origin[1])
	return rl.NewVector3(localX, float32(origin[2]-terrain.centerWorldZ), localZ)
}

func loadTreeInstances(treePath string, terrain *terrainData) ([]treeInstance, error) {
	file, err := os.Open(treePath)
	if err != nil {
		return nil, fmt.Errorf("open tree file: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read tree file: %w", err)
	}
	if len(rows) <= 1 {
		return nil, nil
	}
	if len(rows[0]) < 4 ||
		!strings.EqualFold(strings.TrimSpace(rows[0][0]), "x") ||
		!strings.EqualFold(strings.TrimSpace(rows[0][1]), "y") ||
		!strings.EqualFold(strings.TrimSpace(rows[0][2]), "h_nmt") ||
		!strings.EqualFold(strings.TrimSpace(rows[0][3]), "height") {
		return nil, errors.New("tree file must start with header: x,y,h_nmt,height")
	}

	instances := make([]treeInstance, 0, len(rows)-1)
	for _, row := range rows[1:] {
		if len(row) < 4 {
			continue
		}

		x, err := strconv.ParseFloat(row[0], 64)
		if err != nil {
			continue
		}
		y, err := strconv.ParseFloat(row[1], 64)
		if err != nil {
			continue
		}
		hNMT, err := strconv.ParseFloat(row[2], 64)
		if err != nil {
			continue
		}
		height, err := strconv.ParseFloat(row[3], 64)
		if err != nil || height <= 0 {
			continue
		}
		if !terrainContainsPoint(terrain, x, y) {
			continue
		}

		localX, localZ := terrainLocalXZ(terrain, x, y)
		baseY := terrainHeightAt(terrain, x, y)
		if baseY == 0 && hNMT > terrain.centerWorldZ {
			baseY = float32(hNMT - terrain.centerWorldZ)
		}
		trunkRadius := clamp32(float32(height)*0.035, 0.12, 0.55)
		crownRadius := clamp32(float32(height)*0.18, 0.9, 4.8)

		instances = append(instances, treeInstance{
			X:           localX,
			Z:           localZ,
			BaseY:       baseY,
			Height:      float32(height),
			TrunkRadius: trunkRadius,
			CrownRadius: crownRadius,
		})
	}

	return instances, nil
}

func terrainContainsPoint(terrain *terrainData, worldX, worldY float64) bool {
	return worldX >= terrain.worldWest &&
		worldX <= terrain.worldEast &&
		worldY >= terrain.worldSouth &&
		worldY <= terrain.worldNorth
}

func terrainHeightAt(terrain *terrainData, worldX, worldY float64) float32 {
	if len(terrain.heightSamples) == 0 || terrain.meshWidth < 2 || terrain.meshHeight < 2 {
		return 0
	}

	widthSpan := terrain.worldEast - terrain.worldWest
	heightSpan := terrain.worldNorth - terrain.worldSouth
	if widthSpan <= 0 || heightSpan <= 0 {
		return 0
	}

	fx := (worldX - terrain.worldWest) / widthSpan * float64(terrain.meshWidth-1)
	fy := (terrain.worldNorth - worldY) / heightSpan * float64(terrain.meshHeight-1)
	fx = clampFloat64(fx, 0, float64(terrain.meshWidth-1))
	fy = clampFloat64(fy, 0, float64(terrain.meshHeight-1))

	x0 := int(math.Floor(fx))
	y0 := int(math.Floor(fy))
	x1 := min(x0+1, terrain.meshWidth-1)
	y1 := min(y0+1, terrain.meshHeight-1)
	tx := fx - float64(x0)
	ty := fy - float64(y0)

	h00 := terrain.heightSamples[y0*terrain.meshWidth+x0]
	h10 := terrain.heightSamples[y0*terrain.meshWidth+x1]
	h01 := terrain.heightSamples[y1*terrain.meshWidth+x0]
	h11 := terrain.heightSamples[y1*terrain.meshWidth+x1]

	top := h00*(1-tx) + h10*tx
	bottom := h01*(1-tx) + h11*tx
	return float32(top*(1-ty) + bottom*ty - terrain.centerWorldZ)
}

func terrainIntersectsBounds(terrain *terrainData, minX, maxX, minY, maxY float64) bool {
	return !(maxX < terrain.worldWest ||
		minX > terrain.worldEast ||
		maxY < terrain.worldSouth ||
		minY > terrain.worldNorth)
}

func terrainLocalXZ(terrain *terrainData, worldX, worldY float64) (float32, float32) {
	return float32(worldX - terrain.centerWorldX), float32(terrain.centerWorldY - worldY)
}

func horizontalDistanceSquared(ax, az, bx, bz float32) float32 {
	dx := ax - bx
	dz := az - bz
	return dx*dx + dz*dz
}

func clamp32(value, minValue, maxValue float32) float32 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func max32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func clampFloat64(value, minValue, maxValue float64) float64 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func treeCrownColor(tree treeInstance) rl.Color {
	seed := int(math.Abs(float64(tree.X*0.37+tree.Z*0.19))) % 24
	return rl.NewColor(uint8(42+seed/2), uint8(105+seed), uint8(50+seed/3), 255)
}

func ensureShrubMaskCache(shrubPath string) (string, error) {
	if _, err := exec.LookPath("gdal_translate"); err != nil {
		return "", errors.New("gdal_translate is required to process shrub masks")
	}

	cacheDir := filepath.Join(filepath.Dir(shrubPath), ".terrain-cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("create cache directory: %w", err)
	}

	baseName := strings.TrimSuffix(filepath.Base(shrubPath), filepath.Ext(shrubPath))
	cachePath := filepath.Join(cacheDir, fmt.Sprintf("%s.png", baseName))

	srcInfo, err := os.Stat(shrubPath)
	if err != nil {
		return "", err
	}
	cacheInfo, err := os.Stat(cachePath)
	if err == nil && !cacheInfo.ModTime().Before(srcInfo.ModTime()) {
		return cachePath, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	cmd := exec.Command(
		"gdal_translate",
		"-q",
		"-of", "PNG",
		shrubPath,
		cachePath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gdal_translate failed: %w\n%s", err, strings.TrimSpace(string(output)))
	}

	return cachePath, nil
}

func loadShrubInstances(shrubPath string, terrain *terrainData) ([]treeInstance, error) {
	west, east, south, north, err := readOrthoBounds(shrubPath)
	if err != nil {
		return nil, fmt.Errorf("read mask bounds: %w", err)
	}

	pngPath, err := ensureShrubMaskCache(shrubPath)
	if err != nil {
		return nil, fmt.Errorf("mask cache: %w", err)
	}

	file, err := os.Open(pngPath)
	if err != nil {
		return nil, fmt.Errorf("open mask png: %w", err)
	}
	defer file.Close()

	img, err := png.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("decode mask png: %w", err)
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width == 0 || height == 0 {
		return nil, nil
	}

	var shrubs []treeInstance

	// Determine density: e.g. 1 shrub roughly every 16 sq meters
	// Reduced by 4x from previous 0.03
	prob := 0.00375

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		worldY := north - (float64(y-bounds.Min.Y)/float64(height))*(north-south)
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			if a > 0 && !(r == 65535 && g == 65535 && b == 65535) {
				// Seed deterministic RNG based on coordinate so it's consistent
				seed := uint64(x)*uint64(width) + uint64(y)*uint64(height)
				rngVal := mixUint32(uint32(seed) ^ 0x9e3779b1)

				if float64(rngVal)/float64(math.MaxUint32) < prob {
					worldX := west + (float64(x-bounds.Min.X)/float64(width))*(east-west)
					localX, localZ := terrainLocalXZ(terrain, worldX, worldY)
					baseY := terrainHeightAtLocal(terrain, localX, localZ)

					// Shrub random properties
					hVal := float32(mixUint32(rngVal^0x85ebca6b)) / float32(math.MaxUint32)
					crVal := float32(mixUint32(rngVal^0xc2b2ae35)) / float32(math.MaxUint32)

					// Max height increased to 10.35m (50% taller than 6.9m), min height 2m
					h := 2.0 + hVal*8.35
					cr := h * (0.15 + crVal*0.2) // narrower crowns for shrubs
					tr := cr * 0.1

					shrubs = append(shrubs, treeInstance{
						X:           localX,
						Z:           localZ,
						BaseY:       baseY,
						Height:      h,
						CrownRadius: cr,
						TrunkRadius: tr,
						IsShrub:     true,
					})
				}
			}
		}
	}

	return shrubs, nil
}
