package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	rl "github.com/gen2brain/raylib-go/raylib"
)

const (
	defaultPropAsset       = "assets/props/parked_cars/Ferarri_Testarossa.glb"
	defaultLinearPropAsset = "assets/props/fences/fence.glb"
	defaultLinearSpacingM  = 2.5
)

type propLayerFile struct {
	Version      int                  `json:"version"`
	Props        []propFileInstance   `json:"props"`
	LinearAssets []linearFileInstance `json:"linear_assets,omitempty"`
}

type propFileInstance struct {
	ID         string   `json:"id"`
	Asset      string   `json:"asset"`
	X          float64  `json:"x"`
	Y          float64  `json:"y"`
	Z          *float64 `json:"z"`
	HeadingDeg float32  `json:"heading_deg"`
	Scale      float32  `json:"scale"`
	Category   string   `json:"category,omitempty"`
	Tags       []string `json:"tags,omitempty"`
}

type propInstance struct {
	ID         string
	Asset      string
	WorldX     float64
	WorldY     float64
	WorldZ     *float64
	HeadingDeg float32
	Scale      float32
	Category   string
	Tags       []string
	SourcePath string
}

type linearPointFile struct {
	X float64  `json:"x"`
	Y float64  `json:"y"`
	Z *float64 `json:"z,omitempty"`
}

type linearFileInstance struct {
	ID               string            `json:"id"`
	Asset            string            `json:"asset"`
	Points           []linearPointFile `json:"points"`
	SpacingM         float32           `json:"spacing_m"`
	Scale            float32           `json:"scale"`
	HeadingOffsetDeg float32           `json:"heading_offset_deg,omitempty"`
	Category         string            `json:"category,omitempty"`
	Tags             []string          `json:"tags,omitempty"`
}

type linearPropPoint struct {
	WorldX float64
	WorldY float64
	WorldZ *float64
}

type linearPropInstance struct {
	ID               string
	Asset            string
	Points           []linearPropPoint
	SpacingM         float32
	Scale            float32
	HeadingOffsetDeg float32
	Category         string
	Tags             []string
	SourcePath       string
}

type propAsset struct {
	Asset  string
	Path   string
	Model  rl.Model
	Bounds rl.BoundingBox
	Radius float32
	Loaded bool
}

func loadPropInstances(mapDef *mapDefinition) ([]propInstance, []linearPropInstance, []error) {
	if mapDef == nil || len(mapDef.PropLayerPaths) == 0 {
		return nil, nil, nil
	}

	var props []propInstance
	var linear []linearPropInstance
	var problems []error
	for _, layerPath := range mapDef.PropLayerPaths {
		layerProps, layerLinear, err := loadPropLayer(layerPath)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		props = append(props, layerProps...)
		linear = append(linear, layerLinear...)
	}
	return props, linear, problems
}

func discoverPropAssets(mapDef *mapDefinition, props []propInstance, linear []linearPropInstance) []string {
	seen := map[string]bool{}
	var assets []string
	add := func(asset string) {
		asset = filepath.ToSlash(strings.TrimSpace(asset))
		if asset == "" || seen[asset] {
			return
		}
		seen[asset] = true
		assets = append(assets, asset)
	}

	add(defaultPropAsset)
	add(defaultLinearPropAsset)
	for _, prop := range props {
		add(prop.Asset)
	}
	for _, item := range linear {
		add(item.Asset)
	}

	if mapDef != nil && mapDef.ManifestPath != "" {
		baseDir := filepath.Dir(mapDef.ManifestPath)
		root := filepath.Join(baseDir, "assets", "props")
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() {
				return nil
			}
			if !strings.EqualFold(filepath.Ext(path), ".glb") {
				return nil
			}
			rel, relErr := filepath.Rel(baseDir, path)
			if relErr != nil {
				return nil
			}
			add(rel)
			return nil
		})
	}

	sort.Strings(assets)
	return assets
}

func loadPropLayer(layerPath string) ([]propInstance, []linearPropInstance, error) {
	file, err := os.Open(layerPath)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: open prop layer: %w", filepath.Base(layerPath), err)
	}
	defer file.Close()

	var layer propLayerFile
	if err := json.NewDecoder(file).Decode(&layer); err != nil {
		return nil, nil, fmt.Errorf("%s: decode prop layer: %w", filepath.Base(layerPath), err)
	}
	if layer.Version == 0 {
		layer.Version = 1
	}
	if layer.Version != 1 {
		return nil, nil, fmt.Errorf("%s: unsupported prop layer version %d", filepath.Base(layerPath), layer.Version)
	}

	props := make([]propInstance, 0, len(layer.Props))
	for i, item := range layer.Props {
		asset := strings.TrimSpace(item.Asset)
		if asset == "" {
			return nil, nil, fmt.Errorf("%s: prop %d has no asset", filepath.Base(layerPath), i)
		}
		scale := item.Scale
		if scale == 0 {
			scale = 1
		}
		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = fmt.Sprintf("%s_%03d", strings.TrimSuffix(filepath.Base(layerPath), filepath.Ext(layerPath)), i+1)
		}
		props = append(props, propInstance{
			ID:         id,
			Asset:      asset,
			WorldX:     item.X,
			WorldY:     item.Y,
			WorldZ:     item.Z,
			HeadingDeg: item.HeadingDeg,
			Scale:      scale,
			Category:   item.Category,
			Tags:       append([]string(nil), item.Tags...),
			SourcePath: layerPath,
		})
	}

	linear := make([]linearPropInstance, 0, len(layer.LinearAssets))
	for i, item := range layer.LinearAssets {
		asset := strings.TrimSpace(item.Asset)
		if asset == "" {
			return nil, nil, fmt.Errorf("%s: linear asset %d has no asset", filepath.Base(layerPath), i)
		}
		if len(item.Points) < 2 {
			return nil, nil, fmt.Errorf("%s: linear asset %d needs at least two points", filepath.Base(layerPath), i)
		}
		scale := item.Scale
		if scale == 0 {
			scale = 1
		}
		spacing := item.SpacingM
		if spacing <= 0 {
			spacing = defaultLinearSpacingM
		}
		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = fmt.Sprintf("%s_line_%03d", strings.TrimSuffix(filepath.Base(layerPath), filepath.Ext(layerPath)), i+1)
		}
		points := make([]linearPropPoint, 0, len(item.Points))
		for _, point := range item.Points {
			points = append(points, linearPropPoint{
				WorldX: point.X,
				WorldY: point.Y,
				WorldZ: point.Z,
			})
		}
		linear = append(linear, linearPropInstance{
			ID:               id,
			Asset:            asset,
			Points:           points,
			SpacingM:         spacing,
			Scale:            scale,
			HeadingOffsetDeg: item.HeadingOffsetDeg,
			Category:         item.Category,
			Tags:             append([]string(nil), item.Tags...),
			SourcePath:       layerPath,
		})
	}
	return props, linear, nil
}

func savePropInstances(mapDef *mapDefinition, props []propInstance, linear []linearPropInstance) error {
	if mapDef == nil {
		return errors.New("no map definition loaded")
	}
	layerPath := primaryPropLayerPath(mapDef)
	if layerPath == "" {
		return errors.New("map has no prop layer path")
	}
	if err := os.MkdirAll(filepath.Dir(layerPath), 0o755); err != nil {
		return fmt.Errorf("create prop layer directory: %w", err)
	}

	layer := propLayerFile{
		Version: 1,
		Props:   make([]propFileInstance, 0, len(props)),
	}
	for _, prop := range props {
		scale := prop.Scale
		if scale == 0 {
			scale = 1
		}
		layer.Props = append(layer.Props, propFileInstance{
			ID:         prop.ID,
			Asset:      prop.Asset,
			X:          prop.WorldX,
			Y:          prop.WorldY,
			Z:          prop.WorldZ,
			HeadingDeg: normalizeDegrees(prop.HeadingDeg),
			Scale:      scale,
			Category:   prop.Category,
			Tags:       append([]string(nil), prop.Tags...),
		})
	}
	layer.LinearAssets = make([]linearFileInstance, 0, len(linear))
	for _, item := range linear {
		scale := item.Scale
		if scale == 0 {
			scale = 1
		}
		spacing := item.SpacingM
		if spacing <= 0 {
			spacing = defaultLinearSpacingM
		}
		points := make([]linearPointFile, 0, len(item.Points))
		for _, point := range item.Points {
			points = append(points, linearPointFile{
				X: point.WorldX,
				Y: point.WorldY,
				Z: point.WorldZ,
			})
		}
		layer.LinearAssets = append(layer.LinearAssets, linearFileInstance{
			ID:               item.ID,
			Asset:            item.Asset,
			Points:           points,
			SpacingM:         spacing,
			Scale:            scale,
			HeadingOffsetDeg: normalizeDegrees(item.HeadingOffsetDeg),
			Category:         item.Category,
			Tags:             append([]string(nil), item.Tags...),
		})
	}

	data, err := json.MarshalIndent(layer, "", "  ")
	if err != nil {
		return fmt.Errorf("encode prop layer: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(layerPath, data, 0o644); err != nil {
		return fmt.Errorf("write prop layer: %w", err)
	}
	if len(mapDef.PropLayerPaths) == 0 {
		if err := ensureManifestHasPropLayer(mapDef, layerPath); err != nil {
			return err
		}
		mapDef.PropLayerPaths = []string{layerPath}
	}
	return nil
}

func primaryPropLayerPath(mapDef *mapDefinition) string {
	if mapDef == nil || mapDef.ManifestPath == "" {
		return ""
	}
	if len(mapDef.PropLayerPaths) > 0 {
		return mapDef.PropLayerPaths[0]
	}
	return filepath.Join(filepath.Dir(mapDef.ManifestPath), "props", "props.json")
}

func ensureManifestHasPropLayer(mapDef *mapDefinition, layerPath string) error {
	file, err := os.Open(mapDef.ManifestPath)
	if err != nil {
		return fmt.Errorf("open map manifest for prop layer update: %w", err)
	}
	var manifest mapManifest
	decodeErr := json.NewDecoder(file).Decode(&manifest)
	closeErr := file.Close()
	if decodeErr != nil {
		return fmt.Errorf("decode map manifest for prop layer update: %w", decodeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close map manifest: %w", closeErr)
	}

	baseDir := filepath.Dir(mapDef.ManifestPath)
	rel, err := filepath.Rel(baseDir, layerPath)
	if err != nil {
		return fmt.Errorf("make prop layer path relative: %w", err)
	}
	manifest.PropLayers = []string{filepath.ToSlash(rel)}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode map manifest with prop layer: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(mapDef.ManifestPath, data, 0o644); err != nil {
		return fmt.Errorf("write map manifest with prop layer: %w", err)
	}
	return nil
}

func loadPropAssets(mapDef *mapDefinition, props []propInstance, linear []linearPropInstance) (map[string]*propAsset, []error) {
	assets := map[string]*propAsset{}
	if len(props) == 0 && len(linear) == 0 {
		return assets, nil
	}

	seen := map[string]bool{}
	var keys []string
	for _, prop := range props {
		asset := strings.TrimSpace(prop.Asset)
		if asset == "" || seen[asset] {
			continue
		}
		seen[asset] = true
		keys = append(keys, asset)
	}
	for _, item := range linear {
		asset := strings.TrimSpace(item.Asset)
		if asset == "" || seen[asset] {
			continue
		}
		seen[asset] = true
		keys = append(keys, asset)
	}

	sort.Strings(keys)

	var problems []error
	for _, asset := range keys {
		if _, err := ensurePropAssetLoaded(mapDef, assets, asset); err != nil {
			problems = append(problems, err)
		}
	}
	return assets, problems
}

func ensurePropAssetLoaded(mapDef *mapDefinition, assets map[string]*propAsset, asset string) (*propAsset, error) {
	asset = strings.TrimSpace(asset)
	if asset == "" {
		return nil, errors.New("empty prop asset path")
	}
	if assets == nil {
		return nil, errors.New("prop asset cache is nil")
	}
	if loaded, ok := assets[asset]; ok {
		if loaded.Loaded {
			return loaded, nil
		}
		return nil, fmt.Errorf("prop asset %s is not loaded", asset)
	}

	modelPath := propAssetPath(mapDef, asset)
	model := rl.LoadModel(modelPath)
	if !rl.IsModelValid(model) {
		return nil, fmt.Errorf("load prop asset %s: invalid model", asset)
	}

	bounds := rl.GetModelBoundingBox(model)
	pa := &propAsset{
		Asset:  asset,
		Path:   modelPath,
		Model:  model,
		Bounds: bounds,
		Radius: propBoundsRadius(bounds),
		Loaded: true,
	}
	assets[asset] = pa
	return pa, nil
}

func unloadPropAssets(assets map[string]*propAsset) {
	for _, asset := range assets {
		if asset != nil && asset.Loaded {
			rl.UnloadModel(asset.Model)
			asset.Loaded = false
		}
	}
}

func drawProps(terrain *terrainData, objects *sceneObjects) {
	if terrain == nil || objects == nil {
		return
	}
	for i := range objects.Props {
		prop := &objects.Props[i]
		asset := objects.PropAssets[prop.Asset]
		if asset == nil || !asset.Loaded {
			continue
		}
		scale := prop.Scale
		if scale == 0 {
			scale = 1
		}
		pos := propDrawPosition(terrain, prop, asset)
		rl.DrawModelEx(
			asset.Model,
			pos,
			rl.NewVector3(0, 1, 0),
			prop.HeadingDeg,
			rl.NewVector3(scale, scale, scale),
			rl.White,
		)
	}
	for i := range objects.LinearProps {
		drawLinearProp(terrain, objects, &objects.LinearProps[i])
	}
}

func drawLinearProp(terrain *terrainData, objects *sceneObjects, item *linearPropInstance) {
	if terrain == nil || objects == nil || item == nil || len(item.Points) < 2 {
		return
	}
	asset := objects.PropAssets[item.Asset]
	if asset == nil || !asset.Loaded {
		return
	}
	scale := item.Scale
	if scale == 0 {
		scale = 1
	}
	spacing := item.SpacingM
	if spacing <= 0 {
		spacing = defaultLinearSpacingM
	}
	nextDistance := float32(0)
	for i := 0; i < len(item.Points)-1; i++ {
		a := item.Points[i]
		b := item.Points[i+1]
		ax, az := terrainLocalXZ(terrain, a.WorldX, a.WorldY)
		bx, bz := terrainLocalXZ(terrain, b.WorldX, b.WorldY)
		dx := bx - ax
		dz := bz - az
		length := float32(math.Sqrt(float64(dx*dx + dz*dz)))
		if length < 0.01 {
			continue
		}
		heading := float32(math.Atan2(float64(dx), float64(dz)))*180/math.Pi + item.HeadingOffsetDeg
		for nextDistance <= length {
			t := nextDistance / length
			worldX := a.WorldX + (b.WorldX-a.WorldX)*float64(t)
			worldY := a.WorldY + (b.WorldY-a.WorldY)*float64(t)
			base := linearPointLocalPosition(terrain, linearPropPoint{WorldX: worldX, WorldY: worldY})
			pos := propDrawPositionFromBase(base, asset, scale)
			rl.DrawModelEx(
				asset.Model,
				pos,
				rl.NewVector3(0, 1, 0),
				heading,
				rl.NewVector3(scale, scale, scale),
				rl.White,
			)
			nextDistance += spacing
		}
		nextDistance -= length
	}
}

func propAssetPath(mapDef *mapDefinition, asset string) string {
	if filepath.IsAbs(asset) || mapDef == nil || mapDef.ManifestPath == "" {
		return filepath.Clean(asset)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(mapDef.ManifestPath), filepath.FromSlash(asset)))
}

func propLocalPosition(terrain *terrainData, prop *propInstance) rl.Vector3 {
	if terrain == nil || prop == nil {
		return rl.Vector3{}
	}
	localX, localZ := terrainLocalXZ(terrain, prop.WorldX, prop.WorldY)
	localY := terrainHeightAt(terrain, prop.WorldX, prop.WorldY)
	if prop.WorldZ != nil {
		localY = float32(*prop.WorldZ - terrain.centerWorldZ)
	}
	return rl.NewVector3(localX, localY, localZ)
}

func linearPointLocalPosition(terrain *terrainData, point linearPropPoint) rl.Vector3 {
	if terrain == nil {
		return rl.Vector3{}
	}
	localX, localZ := terrainLocalXZ(terrain, point.WorldX, point.WorldY)
	localY := terrainHeightAt(terrain, point.WorldX, point.WorldY)
	if point.WorldZ != nil {
		localY = float32(*point.WorldZ - terrain.centerWorldZ)
	}
	return rl.NewVector3(localX, localY, localZ)
}

func propDrawPosition(terrain *terrainData, prop *propInstance, asset *propAsset) rl.Vector3 {
	pos := propLocalPosition(terrain, prop)
	scale := float32(1)
	if prop != nil && prop.Scale > 0 {
		scale = prop.Scale
	}
	return propDrawPositionFromBase(pos, asset, scale)
}

func propDrawPositionFromBase(base rl.Vector3, asset *propAsset, scale float32) rl.Vector3 {
	if scale == 0 {
		scale = 1
	}
	if asset != nil {
		base.Y -= asset.Bounds.Min.Y * scale
	}
	return base
}

func propSelectionCenter(terrain *terrainData, prop *propInstance, asset *propAsset) rl.Vector3 {
	pos := propDrawPosition(terrain, prop, asset)
	scale := float32(1)
	if prop != nil && prop.Scale > 0 {
		scale = prop.Scale
	}
	if asset != nil {
		centerY := (asset.Bounds.Min.Y + asset.Bounds.Max.Y) * 0.5
		pos.Y += centerY * scale
	}
	return pos
}

func localXZToWorld(terrain *terrainData, localX, localZ float32) (float64, float64) {
	return terrain.centerWorldX + float64(localX), terrain.centerWorldY - float64(localZ)
}

func propBoundsRadius(bounds rl.BoundingBox) float32 {
	halfX := (bounds.Max.X - bounds.Min.X) * 0.5
	halfY := (bounds.Max.Y - bounds.Min.Y) * 0.5
	halfZ := (bounds.Max.Z - bounds.Min.Z) * 0.5
	r := float32(math.Sqrt(float64(halfX*halfX + halfY*halfY + halfZ*halfZ)))
	if r < 1 {
		return 1
	}
	return r
}

func normalizeDegrees(v float32) float32 {
	for v < 0 {
		v += 360
	}
	for v >= 360 {
		v -= 360
	}
	return v
}
