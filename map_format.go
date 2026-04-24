package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	rl "github.com/gen2brain/raylib-go/raylib"
)

type mapManifest struct {
	Version      int                `json:"version"`
	Name         string             `json:"name"`
	Simulation   string             `json:"simulation,omitempty"`
	RaylibCenter *mapManifestCenter `json:"raylib_center,omitempty"`
	Tiles        []mapManifestTile  `json:"tiles"`
}

type mapManifestTile struct {
	DEM   string `json:"dem"`
	Ortho string `json:"ortho"`
}

type mapManifestCenter struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z,omitempty"`
}

type mapDefinition struct {
	ManifestPath     string
	Name             string
	Simulation       string
	Tiles            []terrainTileSource
	RaylibCenterX    float64
	RaylibCenterY    float64
	RaylibCenterZ    float64
	HasRaylibCenterZ bool
}

type terrainTileSource struct {
	DEMPath   string
	OrthoPath string
	West      float64
	East      float64
	South     float64
	North     float64
}

type preparedTerrainSource struct {
	tiles      []terrainTileSource
	heights    []float64
	valid      []bool
	crop       cropRect
	minHeight  float64
	maxHeight  float64
	worldWest  float64
	worldEast  float64
	worldSouth float64
	worldNorth float64
	centerX    float64
	centerY    float64
	centerZ    float64
}

func loadMapDefinition(mapPath string) (*mapDefinition, error) {
	manifestPath := mapPath
	info, err := os.Stat(mapPath)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		manifestPath = filepath.Join(mapPath, "map.json")
	}

	file, err := os.Open(manifestPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var manifest mapManifest
	if err := json.NewDecoder(file).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode map manifest: %w", err)
	}
	if manifest.Version == 0 {
		manifest.Version = 1
	}
	if manifest.Version != 1 {
		return nil, fmt.Errorf("unsupported map manifest version %d", manifest.Version)
	}
	if len(manifest.Tiles) == 0 {
		return nil, errors.New("map manifest contains no tiles")
	}

	baseDir := filepath.Dir(manifestPath)
	tiles := make([]terrainTileSource, 0, len(manifest.Tiles))
	for i, tile := range manifest.Tiles {
		if tile.DEM == "" || tile.Ortho == "" {
			return nil, fmt.Errorf("map tile %d is missing dem or ortho path", i)
		}

		demPath := filepath.Clean(filepath.Join(baseDir, tile.DEM))
		orthoPath := filepath.Clean(filepath.Join(baseDir, tile.Ortho))
		meta, err := readDEMMetadata(demPath)
		if err != nil {
			return nil, fmt.Errorf("read DEM metadata for tile %d: %w", i, err)
		}
		west, east, south, north := demWorldBounds(meta)
		tiles = append(tiles, terrainTileSource{
			DEMPath:   demPath,
			OrthoPath: orthoPath,
			West:      west,
			East:      east,
			South:     south,
			North:     north,
		})
	}

	name := manifest.Name
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(manifestPath), filepath.Ext(manifestPath))
	}
	mapDef := &mapDefinition{
		ManifestPath: manifestPath,
		Name:         name,
		Simulation:   manifest.Simulation,
		Tiles:        tiles,
	}
	if manifest.RaylibCenter != nil {
		mapDef.RaylibCenterX = manifest.RaylibCenter.X
		mapDef.RaylibCenterY = manifest.RaylibCenter.Y
		mapDef.RaylibCenterZ = manifest.RaylibCenter.Z
		mapDef.HasRaylibCenterZ = true
	}

	return mapDef, nil
}

func prepareTerrainSource(mapDef *mapDefinition, meshMaxDim int) (*preparedTerrainSource, error) {
	if mapDef == nil || len(mapDef.Tiles) == 0 {
		return nil, errors.New("map contains no terrain tiles")
	}

	unionWest := math.MaxFloat64
	unionSouth := math.MaxFloat64
	unionEast := -math.MaxFloat64
	unionNorth := -math.MaxFloat64

	for _, tile := range mapDef.Tiles {
		unionWest = math.Min(unionWest, tile.West)
		unionSouth = math.Min(unionSouth, tile.South)
		unionEast = math.Max(unionEast, tile.East)
		unionNorth = math.Max(unionNorth, tile.North)
	}
	if unionEast <= unionWest || unionNorth <= unionSouth {
		return nil, errors.New("map tiles do not define a valid world extent")
	}

	targetW, targetH := fitDimensionsBySpan(unionEast-unionWest, unionNorth-unionSouth, meshMaxDim)
	sums := make([]float64, targetW*targetH)
	counts := make([]int, targetW*targetH)
	valid := make([]bool, targetW*targetH)

	for _, tile := range mapDef.Tiles {
		meta, err := readDEMMetadata(tile.DEMPath)
		if err != nil {
			return nil, err
		}
		if err := accumulateDEMIntoGrid(tile.DEMPath, meta, unionWest, unionEast, unionSouth, unionNorth, targetW, targetH, sums, counts, valid); err != nil {
			return nil, err
		}
	}

	crop, ok := findValidCrop(valid, targetW, targetH)
	if !ok {
		return nil, errors.New("map contains no valid height samples")
	}

	cropW := crop.maxX - crop.minX + 1
	cropH := crop.maxY - crop.minY + 1
	heights := make([]float64, cropW*cropH)
	croppedValid := make([]bool, cropW*cropH)
	minHeight := math.MaxFloat64
	maxHeight := -math.MaxFloat64

	for y := crop.minY; y <= crop.maxY; y++ {
		for x := crop.minX; x <= crop.maxX; x++ {
			srcIdx := y*targetW + x
			dstIdx := (y-crop.minY)*cropW + (x - crop.minX)
			if counts[srcIdx] == 0 {
				continue
			}
			avg := sums[srcIdx] / float64(counts[srcIdx])
			heights[dstIdx] = avg
			croppedValid[dstIdx] = true
			minHeight = math.Min(minHeight, avg)
			maxHeight = math.Max(maxHeight, avg)
		}
	}

	widthSpan := unionEast - unionWest
	heightSpan := unionNorth - unionSouth
	worldWest := unionWest + (float64(crop.minX)/float64(targetW-1))*widthSpan
	worldEast := unionWest + (float64(crop.maxX)/float64(targetW-1))*widthSpan
	worldNorth := unionNorth - (float64(crop.minY)/float64(targetH-1))*heightSpan
	worldSouth := unionNorth - (float64(crop.maxY)/float64(targetH-1))*heightSpan

	return &preparedTerrainSource{
		tiles:      mapDef.Tiles,
		heights:    heights,
		valid:      croppedValid,
		crop:       cropRect{minX: 0, minY: 0, maxX: cropW - 1, maxY: cropH - 1},
		minHeight:  minHeight,
		maxHeight:  maxHeight,
		worldWest:  worldWest,
		worldEast:  worldEast,
		worldSouth: worldSouth,
		worldNorth: worldNorth,
		centerX:    chooseRaylibCenterX(mapDef, worldWest, worldEast),
		centerY:    chooseRaylibCenterY(mapDef, worldSouth, worldNorth),
		centerZ:    chooseRaylibCenterZ(mapDef, minHeight),
	}, nil
}

func chooseRaylibCenterX(mapDef *mapDefinition, worldWest, worldEast float64) float64 {
	if mapDef != nil && mapDef.RaylibCenterX != 0 {
		return mapDef.RaylibCenterX
	}
	return (worldWest + worldEast) * 0.5
}

func chooseRaylibCenterY(mapDef *mapDefinition, worldSouth, worldNorth float64) float64 {
	if mapDef != nil && mapDef.RaylibCenterY != 0 {
		return mapDef.RaylibCenterY
	}
	return (worldSouth + worldNorth) * 0.5
}

func chooseRaylibCenterZ(mapDef *mapDefinition, minHeight float64) float64 {
	if mapDef != nil && mapDef.HasRaylibCenterZ {
		return mapDef.RaylibCenterZ
	}
	return minHeight
}

func readDEMMetadata(path string) (demMetadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return demMetadata{}, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Split(bufio.ScanWords)
	scanner.Buffer(make([]byte, 1024), 1024*1024)

	headerValues := make(map[string]string, 6)
	for len(headerValues) < 6 {
		if !scanner.Scan() {
			return demMetadata{}, errors.New("unexpected EOF while reading DEM header keys")
		}
		key := strings.ToLower(scanner.Text())
		if !scanner.Scan() {
			return demMetadata{}, fmt.Errorf("missing value for DEM header key %q", key)
		}
		headerValues[key] = scanner.Text()
	}
	if err := scanner.Err(); err != nil {
		return demMetadata{}, err
	}

	cols, err := strconv.Atoi(headerValues["ncols"])
	if err != nil {
		return demMetadata{}, fmt.Errorf("invalid ncols: %w", err)
	}
	rows, err := strconv.Atoi(headerValues["nrows"])
	if err != nil {
		return demMetadata{}, fmt.Errorf("invalid nrows: %w", err)
	}
	cellSize, err := strconv.ParseFloat(headerValues["cellsize"], 64)
	if err != nil {
		return demMetadata{}, fmt.Errorf("invalid cellsize: %w", err)
	}
	noData, err := strconv.ParseFloat(headerValues["nodata_value"], 64)
	if err != nil {
		return demMetadata{}, fmt.Errorf("invalid nodata_value: %w", err)
	}

	meta := demMetadata{
		cols:     cols,
		rows:     rows,
		cellSize: cellSize,
		noData:   noData,
	}
	if xll, ok := headerValues["xllcenter"]; ok {
		yll, ok := headerValues["yllcenter"]
		if !ok {
			return demMetadata{}, errors.New("DEM header missing yllcenter")
		}
		meta.xOrigin, err = strconv.ParseFloat(xll, 64)
		if err != nil {
			return demMetadata{}, fmt.Errorf("invalid xllcenter: %w", err)
		}
		meta.yOrigin, err = strconv.ParseFloat(yll, 64)
		if err != nil {
			return demMetadata{}, fmt.Errorf("invalid yllcenter: %w", err)
		}
		meta.originIsCenter = true
	} else if xll, ok := headerValues["xllcorner"]; ok {
		yll, ok := headerValues["yllcorner"]
		if !ok {
			return demMetadata{}, errors.New("DEM header missing yllcorner")
		}
		meta.xOrigin, err = strconv.ParseFloat(xll, 64)
		if err != nil {
			return demMetadata{}, fmt.Errorf("invalid xllcorner: %w", err)
		}
		meta.yOrigin, err = strconv.ParseFloat(yll, 64)
		if err != nil {
			return demMetadata{}, fmt.Errorf("invalid yllcorner: %w", err)
		}
	} else {
		return demMetadata{}, errors.New("DEM header missing xllcenter/xllcorner")
	}

	return meta, nil
}

func accumulateDEMIntoGrid(path string, meta demMetadata, worldWest, worldEast, worldSouth, worldNorth float64, targetW, targetH int, sums []float64, counts []int, valid []bool) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Split(bufio.ScanWords)
	scanner.Buffer(make([]byte, 1024), 1024*1024)

	for i := 0; i < 12; i++ {
		if !scanner.Scan() {
			return errors.New("unexpected EOF while skipping DEM header")
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	widthSpan := worldEast - worldWest
	heightSpan := worldNorth - worldSouth
	tileWest, _, _, tileNorth := demWorldBounds(meta)

	for row := 0; row < meta.rows; row++ {
		cellY := tileNorth - (float64(row)+0.5)*meta.cellSize
		fy := (worldNorth - cellY) / heightSpan * float64(targetH-1)
		ty := int(math.Round(fy))
		if ty < 0 || ty >= targetH {
			ty = min(max(ty, 0), targetH-1)
		}

		for col := 0; col < meta.cols; col++ {
			if !scanner.Scan() {
				return fmt.Errorf("unexpected EOF while reading DEM values at row %d col %d", row, col)
			}
			value, err := strconv.ParseFloat(scanner.Text(), 64)
			if err != nil {
				return fmt.Errorf("invalid DEM value at row %d col %d: %w", row, col, err)
			}
			if value == meta.noData {
				continue
			}

			cellX := tileWest + (float64(col)+0.5)*meta.cellSize
			fx := (cellX - worldWest) / widthSpan * float64(targetW-1)
			tx := int(math.Round(fx))
			if tx < 0 || tx >= targetW {
				tx = min(max(tx, 0), targetW-1)
			}

			idx := ty*targetW + tx
			sums[idx] += value
			counts[idx]++
			valid[idx] = true
		}
	}

	return scanner.Err()
}

func loadOrthophotoMosaicImage(tiles []terrainTileSource, worldWest, worldEast, worldSouth, worldNorth float64, textureMaxDim int) (*rl.Image, int, int, error) {
	textureW, textureH := fitDimensionsBySpan(worldEast-worldWest, worldNorth-worldSouth, textureMaxDim)
	canvas := image.NewRGBA(image.Rect(0, 0, textureW, textureH))

	for _, tile := range tiles {
		interWest := math.Max(worldWest, tile.West)
		interEast := math.Min(worldEast, tile.East)
		interSouth := math.Max(worldSouth, tile.South)
		interNorth := math.Min(worldNorth, tile.North)
		if interEast <= interWest || interNorth <= interSouth {
			continue
		}

		dst := worldBoundsToImageRect(interWest, interEast, interSouth, interNorth, worldWest, worldEast, worldSouth, worldNorth, textureW, textureH)
		if dst.Dx() < 2 || dst.Dy() < 2 {
			continue
		}

		img, err := loadOrthophotoImageExact(tile.OrthoPath, interWest, interEast, interSouth, interNorth, dst.Dx(), dst.Dy())
		if err != nil {
			return nil, 0, 0, err
		}
		draw.Draw(canvas, dst, img, image.Point{}, draw.Src)
	}

	return rl.NewImageFromImage(canvas), textureW, textureH, nil
}

func loadOrthophotoImageExact(orthoPath string, worldWest, worldEast, worldSouth, worldNorth float64, width, height int) (image.Image, error) {
	cachePath, err := ensureOrthophotoCache(orthoPath, worldWest, worldEast, worldSouth, worldNorth, width, height)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(cachePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	img, err := png.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("decode cached orthophoto PNG: %w", err)
	}
	return img, nil
}

func worldBoundsToImageRect(tileWest, tileEast, tileSouth, tileNorth, worldWest, worldEast, worldSouth, worldNorth float64, width, height int) image.Rectangle {
	widthSpan := worldEast - worldWest
	heightSpan := worldNorth - worldSouth

	x0 := int(math.Round((tileWest - worldWest) / widthSpan * float64(width)))
	x1 := int(math.Round((tileEast - worldWest) / widthSpan * float64(width)))
	y0 := int(math.Round((worldNorth - tileNorth) / heightSpan * float64(height)))
	y1 := int(math.Round((worldNorth - tileSouth) / heightSpan * float64(height)))

	x0 = clampInt(x0, 0, width)
	x1 = clampInt(x1, 0, width)
	y0 = clampInt(y0, 0, height)
	y1 = clampInt(y1, 0, height)
	if x1 <= x0 {
		x1 = min(width, x0+1)
	}
	if y1 <= y0 {
		y1 = min(height, y0+1)
	}

	return image.Rect(x0, y0, x1, y1)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
