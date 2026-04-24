package main

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	rl "github.com/gen2brain/raylib-go/raylib"
)

type demMetadata struct {
	cols           int
	rows           int
	cellSize       float64
	noData         float64
	xOrigin        float64
	yOrigin        float64
	originIsCenter bool
}

type cropRect struct {
	minX int
	minY int
	maxX int
	maxY int
}

type terrainData struct {
	heightImage   *rl.Image
	textureImage  *rl.Image
	model         rl.Model
	texture       rl.Texture2D
	sourceTiles   []terrainTileSource
	heightSamples []float64
	position      rl.Vector3
	centerWorldX  float64
	centerWorldY  float64
	centerWorldZ  float64
	widthMeters   float32
	depthMeters   float32
	heightMeters  float32
	heightMin     float64
	heightMax     float64
	meshWidth     int
	meshHeight    int
	textureWidth  int
	textureHeight int
	worldWest     float64
	worldEast     float64
	worldSouth    float64
	worldNorth    float64
}

func loadTerrain(mapDef *mapDefinition, meshMaxDim int, textureMaxDim int) (*terrainData, error) {
	source, err := prepareTerrainSource(mapDef, meshMaxDim)
	if err != nil {
		return nil, err
	}

	cropW := source.crop.maxX - source.crop.minX + 1
	cropH := source.crop.maxY - source.crop.minY + 1
	if cropW < 2 || cropH < 2 {
		return nil, errors.New("cropped DEM is too small to render")
	}

	fillMissingWithNearest(source.heights, source.valid, cropW, cropH)

	heightImg := image.NewGray(image.Rect(0, 0, cropW, cropH))
	heightRange := source.maxHeight - source.minHeight
	if heightRange <= 0 {
		heightRange = 1
	}

	for i, v := range source.heights {
		normalized := (v - source.minHeight) / heightRange
		if normalized < 0 {
			normalized = 0
		}
		if normalized > 1 {
			normalized = 1
		}
		heightImg.Pix[i] = uint8(math.Round(normalized * 255))
	}

	heightImage := rl.NewImageFromImage(heightImg)

	textureImage, textureW, textureH, err := loadOrthophotoMosaicImage(
		source.tiles,
		source.worldWest,
		source.worldEast,
		source.worldSouth,
		source.worldNorth,
		textureMaxDim,
	)
	if err != nil {
		rl.UnloadImage(heightImage)
		return nil, err
	}

	meshWidthMeters := float32(source.worldEast - source.worldWest)
	meshDepthMeters := float32(source.worldNorth - source.worldSouth)
	meshHeightMeters := float32(source.maxHeight - source.minHeight)
	if meshHeightMeters < 1 {
		meshHeightMeters = 1
	}

	mesh := rl.GenMeshHeightmap(*heightImage, rl.NewVector3(meshWidthMeters, meshHeightMeters, meshDepthMeters))
	model := rl.LoadModelFromMesh(mesh)
	texture := rl.LoadTextureFromImage(textureImage)
	rl.GenTextureMipmaps(&texture)
	rl.SetTextureFilter(texture, rl.FilterAnisotropic16x)
	rl.SetTextureWrap(texture, rl.WrapClamp)

	materials := model.GetMaterials()
	if len(materials) == 0 {
		rl.UnloadTexture(texture)
		rl.UnloadImage(textureImage)
		rl.UnloadImage(heightImage)
		rl.UnloadModel(model)
		return nil, errors.New("generated model has no material slots")
	}
	rl.SetMaterialTexture(&materials[0], rl.MapAlbedo, texture)

	return &terrainData{
		heightImage:   heightImage,
		textureImage:  textureImage,
		model:         model,
		texture:       texture,
		sourceTiles:   source.tiles,
		heightSamples: source.heights,
		position: rl.NewVector3(
			float32(source.worldWest-source.centerX),
			float32(source.minHeight-source.centerZ),
			float32(source.centerY-source.worldNorth),
		),
		centerWorldX:  source.centerX,
		centerWorldY:  source.centerY,
		centerWorldZ:  source.centerZ,
		widthMeters:   meshWidthMeters,
		depthMeters:   meshDepthMeters,
		heightMeters:  meshHeightMeters,
		heightMin:     source.minHeight,
		heightMax:     source.maxHeight,
		meshWidth:     cropW,
		meshHeight:    cropH,
		textureWidth:  textureW,
		textureHeight: textureH,
		worldWest:     source.worldWest,
		worldEast:     source.worldEast,
		worldSouth:    source.worldSouth,
		worldNorth:    source.worldNorth,
	}, nil
}

func unloadTerrain(t *terrainData) {
	if t == nil {
		return
	}
	rl.UnloadModel(t.model)
	rl.UnloadTexture(t.texture)
	if t.heightImage != nil {
		rl.UnloadImage(t.heightImage)
	}
	if t.textureImage != nil {
		rl.UnloadImage(t.textureImage)
	}
}

// terrainHeightAtLocal returns the terrain elevation (raylib Y) at a point
// expressed in raylib/sim local coordinates: localX is raylib X, localZ is
// raylib Z. Returns 0 outside the terrain extent.
func terrainHeightAtLocal(t *terrainData, localX, localZ float32) float32 {
	if t == nil || t.meshWidth < 2 || t.meshHeight < 2 {
		return 0
	}
	fx := float64(localX-t.position.X) / float64(t.widthMeters) * float64(t.meshWidth-1)
	fy := float64(localZ-t.position.Z) / float64(t.depthMeters) * float64(t.meshHeight-1)
	fx = math.Max(0, math.Min(fx, float64(t.meshWidth-1)))
	fy = math.Max(0, math.Min(fy, float64(t.meshHeight-1)))

	x0 := int(math.Floor(fx))
	y0 := int(math.Floor(fy))
	x1 := min(x0+1, t.meshWidth-1)
	y1 := min(y0+1, t.meshHeight-1)
	tx := fx - float64(x0)
	ty := fy - float64(y0)

	h00 := t.heightSamples[y0*t.meshWidth+x0]
	h10 := t.heightSamples[y0*t.meshWidth+x1]
	h01 := t.heightSamples[y1*t.meshWidth+x0]
	h11 := t.heightSamples[y1*t.meshWidth+x1]

	top := h00*(1-tx) + h10*tx
	bottom := h01*(1-tx) + h11*tx
	return float32(top*(1-ty) + bottom*ty - t.centerWorldZ)
}

func ensureOrthophotoCache(orthoPath string, worldWest, worldEast, worldSouth, worldNorth float64, width, height int) (string, error) {
	if _, err := exec.LookPath("gdal_translate"); err != nil {
		return "", errors.New("gdal_translate is required to downsample the GeoTIFF orthophoto")
	}

	cacheDir := filepath.Join(filepath.Dir(orthoPath), ".terrain-cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("create cache directory: %w", err)
	}

	baseName := strings.TrimSuffix(filepath.Base(orthoPath), filepath.Ext(orthoPath))
	cacheKey := fmt.Sprintf("%.3f|%.3f|%.3f|%.3f|%d|%d", worldWest, worldNorth, worldEast, worldSouth, width, height)
	cacheHash := sha1.Sum([]byte(cacheKey))
	cachePath := filepath.Join(cacheDir, fmt.Sprintf("%s-%x.png", baseName, cacheHash))

	srcInfo, err := os.Stat(orthoPath)
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
		"-r", "cubic",
		"-projwin",
		fmt.Sprintf("%.3f", worldWest),
		fmt.Sprintf("%.3f", worldNorth),
		fmt.Sprintf("%.3f", worldEast),
		fmt.Sprintf("%.3f", worldSouth),
		"-outsize", strconv.Itoa(width), strconv.Itoa(height),
		orthoPath,
		cachePath,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gdal_translate failed: %w\n%s", err, strings.TrimSpace(string(output)))
	}

	return cachePath, nil
}

func fillMissingWithNearest(values []float64, valid []bool, width, height int) {
	type point struct {
		x int
		y int
	}

	queue := make([]point, 0, len(values))
	head := 0
	visited := make([]bool, len(valid))

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			idx := y*width + x
			if !valid[idx] {
				continue
			}
			visited[idx] = true
			queue = append(queue, point{x: x, y: y})
		}
	}

	directions := [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}
	for head < len(queue) {
		current := queue[head]
		head++
		currentIdx := current.y*width + current.x

		for _, step := range directions {
			nx := current.x + step[0]
			ny := current.y + step[1]
			if nx < 0 || ny < 0 || nx >= width || ny >= height {
				continue
			}

			nextIdx := ny*width + nx
			if visited[nextIdx] {
				continue
			}

			values[nextIdx] = values[currentIdx]
			valid[nextIdx] = true
			visited[nextIdx] = true
			queue = append(queue, point{x: nx, y: ny})
		}
	}
}

func findValidCrop(valid []bool, width, height int) (cropRect, bool) {
	minX := width
	minY := height
	maxX := -1
	maxY := -1

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if !valid[y*width+x] {
				continue
			}
			if x < minX {
				minX = x
			}
			if y < minY {
				minY = y
			}
			if x > maxX {
				maxX = x
			}
			if y > maxY {
				maxY = y
			}
		}
	}

	if maxX < minX || maxY < minY {
		return cropRect{}, false
	}

	return cropRect{minX: minX, minY: minY, maxX: maxX, maxY: maxY}, true
}

func demWorldBounds(meta demMetadata) (west, east, south, north float64) {
	west = meta.xOrigin
	south = meta.yOrigin
	if meta.originIsCenter {
		west -= meta.cellSize / 2
		south -= meta.cellSize / 2
	}

	east = west + float64(meta.cols)*meta.cellSize
	north = south + float64(meta.rows)*meta.cellSize
	return west, east, south, north
}

func fitDimensionsBySpan(spanX, spanY float64, targetMaxDim int) (int, int) {
	if spanX <= 0 || spanY <= 0 {
		return 2, 2
	}

	scale := float64(targetMaxDim) / math.Max(spanX, spanY)
	w := int(math.Round(spanX * scale))
	h := int(math.Round(spanY * scale))
	if w < 2 {
		w = 2
	}
	if h < 2 {
		h = 2
	}
	return w, h
}

