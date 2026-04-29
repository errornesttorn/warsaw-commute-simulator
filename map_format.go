package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type mapManifest struct {
	Version      int                `json:"version"`
	Name         string             `json:"name"`
	Simulation   string             `json:"simulation,omitempty"`
	RaylibCenter *mapManifestCenter `json:"raylib_center,omitempty"`
	// Tiles is the legacy paired DEM+ortho format. Still accepted but new
	// maps should prefer the independent Dems/Orthos glob lists.
	Tiles        []mapManifestTile `json:"tiles,omitempty"`
	Dems         []string          `json:"dems,omitempty"`
	Orthos       []string          `json:"orthos,omitempty"`
	BuildingGLBs []string          `json:"building_glbs,omitempty"`
	TreeFiles    []string          `json:"tree_files,omitempty"`
	ShrubMasks   []string          `json:"shrub_masks,omitempty"`
	PropLayers   []string          `json:"prop_layers,omitempty"`
	ObjectLayers []string          `json:"object_layers,omitempty"`
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
	DEMTiles         []demTileSource
	OrthoTiles       []orthoTileSource
	BuildingGLBPaths []string
	TreePaths        []string
	ShrubMaskPaths   []string
	PropLayerPaths   []string
	RaylibCenterX    float64
	RaylibCenterY    float64
	RaylibCenterZ    float64
	HasRaylibCenterZ bool
}

type demTileSource struct {
	Path  string
	West  float64
	East  float64
	South float64
	North float64
}

type orthoTileSource struct {
	Path  string
	West  float64
	East  float64
	South float64
	North float64
}

type preparedTerrainSource struct {
	demTiles   []demTileSource
	orthoTiles []orthoTileSource
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
	baseDir := filepath.Dir(manifestPath)

	// Combine the legacy paired Tiles list with the new independent
	// Dems/Orthos glob lists. The DEM/ortho coverage need not match.
	demEntries := append([]string(nil), manifest.Dems...)
	orthoEntries := append([]string(nil), manifest.Orthos...)
	for _, tile := range manifest.Tiles {
		if tile.DEM != "" {
			demEntries = append(demEntries, tile.DEM)
		}
		if tile.Ortho != "" {
			orthoEntries = append(orthoEntries, tile.Ortho)
		}
	}

	demPaths, err := resolveMapFilePatterns(baseDir, demEntries)
	if err != nil {
		return nil, fmt.Errorf("resolve DEM files: %w", err)
	}
	if len(demPaths) == 0 {
		return nil, errors.New("map manifest contains no DEM (.asc) files")
	}
	orthoPaths, err := resolveMapFilePatterns(baseDir, orthoEntries)
	if err != nil {
		return nil, fmt.Errorf("resolve ortho files: %w", err)
	}

	demTiles := make([]demTileSource, 0, len(demPaths))
	for _, demPath := range demPaths {
		meta, err := readDEMMetadata(demPath)
		if err != nil {
			return nil, fmt.Errorf("read DEM metadata for %s: %w", filepath.Base(demPath), err)
		}
		west, east, south, north := demWorldBounds(meta)
		demTiles = append(demTiles, demTileSource{
			Path: demPath, West: west, East: east, South: south, North: north,
		})
	}

	orthoTiles := make([]orthoTileSource, 0, len(orthoPaths))
	for _, orthoPath := range orthoPaths {
		west, east, south, north, err := readOrthoBounds(orthoPath)
		if err != nil {
			return nil, fmt.Errorf("read ortho bounds for %s: %w", filepath.Base(orthoPath), err)
		}
		orthoTiles = append(orthoTiles, orthoTileSource{
			Path: orthoPath, West: west, East: east, South: south, North: north,
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
		DEMTiles:     demTiles,
		OrthoTiles:   orthoTiles,
	}
	buildingGLBPaths, err := resolveMapFilePatterns(baseDir, manifest.BuildingGLBs)
	if err != nil {
		return nil, fmt.Errorf("resolve building GLB files: %w", err)
	}
	mapDef.BuildingGLBPaths = buildingGLBPaths
	treePaths, err := resolveMapFilePatterns(baseDir, manifest.TreeFiles)
	if err != nil {
		return nil, fmt.Errorf("resolve tree files: %w", err)
	}
	mapDef.TreePaths = treePaths

	shrubMaskPaths, err := resolveMapFilePatterns(baseDir, manifest.ShrubMasks)
	if err != nil {
		return nil, fmt.Errorf("resolve shrub mask files: %w", err)
	}
	mapDef.ShrubMaskPaths = shrubMaskPaths

	propLayerEntries := append([]string(nil), manifest.PropLayers...)
	propLayerEntries = append(propLayerEntries, manifest.ObjectLayers...)
	propLayerPaths, err := resolveMapFilePatterns(baseDir, propLayerEntries)
	if err != nil {
		return nil, fmt.Errorf("resolve prop layer files: %w", err)
	}
	mapDef.PropLayerPaths = propLayerPaths

	if manifest.RaylibCenter != nil {
		mapDef.RaylibCenterX = manifest.RaylibCenter.X
		mapDef.RaylibCenterY = manifest.RaylibCenter.Y
		mapDef.RaylibCenterZ = manifest.RaylibCenter.Z
		mapDef.HasRaylibCenterZ = true
	}

	return mapDef, nil
}

func resolveMapFilePatterns(baseDir string, entries []string) ([]string, error) {
	var paths []string
	seen := map[string]bool{}

	for _, entry := range entries {
		if strings.TrimSpace(entry) == "" {
			continue
		}

		pattern := entry
		if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(baseDir, pattern)
		}
		pattern = filepath.Clean(pattern)

		var matches []string
		if hasGlobMeta(pattern) {
			var err error
			matches, err = filepath.Glob(pattern)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", entry, err)
			}
			if len(matches) == 0 {
				return nil, fmt.Errorf("%s: no matches", entry)
			}
		} else {
			matches = []string{pattern}
		}

		sort.Strings(matches)
		for _, match := range matches {
			match = filepath.Clean(match)
			if seen[match] {
				continue
			}
			seen[match] = true
			paths = append(paths, match)
		}
	}

	return paths, nil
}

func hasGlobMeta(path string) bool {
	return strings.ContainsAny(path, "*?[")
}

func prepareTerrainSource(mapDef *mapDefinition, meshMaxDim int) (*preparedTerrainSource, error) {
	if mapDef == nil || len(mapDef.DEMTiles) == 0 {
		return nil, errors.New("map contains no DEM tiles")
	}

	// World extent is driven by the DEMs only — ortho-only areas have no
	// surface to texture. Orthos that fall outside this extent are simply
	// ignored when building the mosaic.
	unionWest := math.MaxFloat64
	unionSouth := math.MaxFloat64
	unionEast := -math.MaxFloat64
	unionNorth := -math.MaxFloat64

	for _, tile := range mapDef.DEMTiles {
		unionWest = math.Min(unionWest, tile.West)
		unionSouth = math.Min(unionSouth, tile.South)
		unionEast = math.Max(unionEast, tile.East)
		unionNorth = math.Max(unionNorth, tile.North)
	}
	if unionEast <= unionWest || unionNorth <= unionSouth {
		return nil, errors.New("map DEMs do not define a valid world extent")
	}

	targetW, targetH := fitDimensionsBySpan(unionEast-unionWest, unionNorth-unionSouth, meshMaxDim)

	cachePath, cacheKeyErr := terrainGridCachePath(mapDef, meshMaxDim, targetW, targetH, unionWest, unionEast, unionSouth, unionNorth)
	if cacheKeyErr == nil {
		if cached, ok := loadTerrainGridCache(cachePath, targetW, targetH); ok {
			return finishPreparedTerrainSource(mapDef, cached.sums, cached.counts, cached.valid, targetW, targetH, unionWest, unionEast, unionSouth, unionNorth)
		}
	}

	sums := make([]float64, targetW*targetH)
	counts := make([]int, targetW*targetH)
	valid := make([]bool, targetW*targetH)

	for _, tile := range mapDef.DEMTiles {
		meta, err := readDEMMetadata(tile.Path)
		if err != nil {
			return nil, err
		}
		if err := accumulateDEMIntoGrid(tile.Path, meta, unionWest, unionEast, unionSouth, unionNorth, targetW, targetH, sums, counts, valid); err != nil {
			return nil, err
		}
	}

	if cacheKeyErr == nil {
		_ = saveTerrainGridCache(cachePath, sums, counts, valid, targetW, targetH)
	}

	return finishPreparedTerrainSource(mapDef, sums, counts, valid, targetW, targetH, unionWest, unionEast, unionSouth, unionNorth)
}

func finishPreparedTerrainSource(mapDef *mapDefinition, sums []float64, counts []int, valid []bool, targetW, targetH int, unionWest, unionEast, unionSouth, unionNorth float64) (*preparedTerrainSource, error) {
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
		demTiles:   mapDef.DEMTiles,
		orthoTiles: mapDef.OrthoTiles,
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

// orthoFallbackColor fills any part of the terrain canvas that has no ortho
// coverage. It picks a neutral grey-green so DEM-only regions render as flat
// terrain rather than transparent black through raylib's default shader.
var orthoFallbackColor = color.RGBA{R: 90, G: 100, B: 86, A: 255}

func buildOrthoMosaic(tiles []orthoTileSource, worldWest, worldEast, worldSouth, worldNorth float64, textureMaxDim int) (*image.RGBA, int, int, error) {
	textureW, textureH := fitDimensionsBySpan(worldEast-worldWest, worldNorth-worldSouth, textureMaxDim)
	canvas := image.NewRGBA(image.Rect(0, 0, textureW, textureH))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: orthoFallbackColor}, image.Point{}, draw.Src)

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

		img, err := loadOrthophotoImageExact(tile.Path, interWest, interEast, interSouth, interNorth, dst.Dx(), dst.Dy())
		if err != nil {
			return nil, 0, 0, err
		}
		draw.Draw(canvas, dst, img, image.Point{}, draw.Src)
	}

	return canvas, textureW, textureH, nil
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

type terrainGridCacheData struct {
	sums   []float64
	counts []int
	valid  []bool
}

const terrainGridCacheMagic uint32 = 0x44454d31 // "DEM1"

func terrainGridCachePath(mapDef *mapDefinition, meshMaxDim, targetW, targetH int, west, east, south, north float64) (string, error) {
	if mapDef == nil || len(mapDef.DEMTiles) == 0 {
		return "", errors.New("no map")
	}
	baseDir := filepath.Dir(mapDef.ManifestPath)
	cacheDir := filepath.Join(baseDir, ".terrain-cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}

	h := sha1.New()
	fmt.Fprintf(h, "v2|%d|%d|%d|%.6f|%.6f|%.6f|%.6f", meshMaxDim, targetW, targetH, west, east, south, north)
	for _, tile := range mapDef.DEMTiles {
		info, err := os.Stat(tile.Path)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "|%s|%d|%d", filepath.Base(tile.Path), info.Size(), info.ModTime().UnixNano())
	}
	return filepath.Join(cacheDir, fmt.Sprintf("dem-grid-%x.bin", h.Sum(nil))), nil
}

func loadTerrainGridCache(path string, targetW, targetH int) (terrainGridCacheData, bool) {
	file, err := os.Open(path)
	if err != nil {
		return terrainGridCacheData{}, false
	}
	defer file.Close()

	var header struct {
		Magic   uint32
		Version uint32
		Width   uint32
		Height  uint32
	}
	if err := binary.Read(file, binary.LittleEndian, &header); err != nil {
		return terrainGridCacheData{}, false
	}
	if header.Magic != terrainGridCacheMagic || header.Version != 1 ||
		int(header.Width) != targetW || int(header.Height) != targetH {
		return terrainGridCacheData{}, false
	}

	n := targetW * targetH
	sums := make([]float64, n)
	counts := make([]int, n)
	valid := make([]bool, n)

	if err := binary.Read(file, binary.LittleEndian, sums); err != nil {
		return terrainGridCacheData{}, false
	}
	counts32 := make([]int32, n)
	if err := binary.Read(file, binary.LittleEndian, counts32); err != nil {
		return terrainGridCacheData{}, false
	}
	for i, v := range counts32 {
		counts[i] = int(v)
	}
	validBytes := make([]byte, n)
	if _, err := io.ReadFull(file, validBytes); err != nil {
		return terrainGridCacheData{}, false
	}
	for i, v := range validBytes {
		valid[i] = v != 0
	}

	return terrainGridCacheData{sums: sums, counts: counts, valid: valid}, true
}

func saveTerrainGridCache(path string, sums []float64, counts []int, valid []bool, targetW, targetH int) error {
	tmp := path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() {
		file.Close()
		os.Remove(tmp)
	}()

	w := bufio.NewWriter(file)
	header := struct {
		Magic   uint32
		Version uint32
		Width   uint32
		Height  uint32
	}{terrainGridCacheMagic, 1, uint32(targetW), uint32(targetH)}
	if err := binary.Write(w, binary.LittleEndian, header); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, sums); err != nil {
		return err
	}
	counts32 := make([]int32, len(counts))
	for i, v := range counts {
		counts32[i] = int32(v)
	}
	if err := binary.Write(w, binary.LittleEndian, counts32); err != nil {
		return err
	}
	validBytes := make([]byte, len(valid))
	for i, v := range valid {
		if v {
			validBytes[i] = 1
		}
	}
	if _, err := w.Write(validBytes); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readOrthoBounds returns the projected world bounds of a GeoTIFF orthophoto
// by parsing `gdalinfo -json`. We rely on gdal already being a hard dependency
// (used elsewhere for orthophoto downsampling) rather than implementing
// GeoTIFF tag parsing inline. Assumes a north-up georeference, which is the
// case for every dataset produced by the standard Polish geoportal exports
// this game targets.
func readOrthoBounds(path string) (west, east, south, north float64, err error) {
	if _, lookErr := exec.LookPath("gdalinfo"); lookErr != nil {
		return 0, 0, 0, 0, errors.New("gdalinfo is required to read orthophoto bounds")
	}
	out, runErr := exec.Command("gdalinfo", "-json", path).Output()
	if runErr != nil {
		return 0, 0, 0, 0, fmt.Errorf("gdalinfo failed: %w", runErr)
	}
	var info struct {
		CornerCoordinates struct {
			UpperLeft  [2]float64 `json:"upperLeft"`
			LowerRight [2]float64 `json:"lowerRight"`
		} `json:"cornerCoordinates"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("parse gdalinfo output: %w", err)
	}
	west = info.CornerCoordinates.UpperLeft[0]
	north = info.CornerCoordinates.UpperLeft[1]
	east = info.CornerCoordinates.LowerRight[0]
	south = info.CornerCoordinates.LowerRight[1]
	if east <= west || north <= south {
		return 0, 0, 0, 0, fmt.Errorf("invalid georeference (corners %.3f,%.3f .. %.3f,%.3f)", west, north, east, south)
	}
	return west, east, south, north, nil
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
