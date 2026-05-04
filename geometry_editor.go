package main

import (
	"fmt"
	"math"

	rl "github.com/gen2brain/raylib-go/raylib"
)

type geometryEditTool int

const (
	geometryToolRaiseLower geometryEditTool = iota
	geometryToolLevel
	geometryToolSmooth
)

const (
	defaultGeometryBrushSize = float32(10)
	geometryBrushMinSize     = float32(1)
	geometryBrushMaxSize     = float32(200)
	geometryRaiseSpeedMPS    = float32(20)
	geometrySmoothRate       = float32(5)

	geometryPanelX     = int32(20)
	geometryPanelY     = int32(34)
	geometrySliderX    = int32(20)
	geometrySliderY    = int32(78)
	geometrySliderW    = int32(220)
	geometrySliderH    = int32(20)
	geometrySliderGrab = int32(14)
)

func (a *App) toggleGeometryEditor() {
	if a.geometryMode {
		a.geometryMode = false
		a.mouseCaptured = true
		a.applyMouseCapture()
		a.setGeometryStatus("")
		return
	}
	if a.terrain == nil || a.mapDef == nil {
		a.setGeometryStatus("load a map before editing geometry")
		return
	}
	a.geometryMode = true
	a.geometryTool = geometryToolRaiseLower
	a.geometryHasTargetElev = false
	a.mouseCaptured = false
	a.paused = true
	a.applyMouseCapture()
	if a.geometryBrushSize <= 0 {
		a.geometryBrushSize = defaultGeometryBrushSize
	}
}

func (a *App) updateGeometryEditor(dt float32) {
	if a.terrain == nil || a.mapDef == nil {
		return
	}
	a.paused = true

	ctrlDown := rl.IsKeyDown(rl.KeyLeftControl) || rl.IsKeyDown(rl.KeyRightControl)
	if ctrlDown && rl.IsKeyPressed(rl.KeyS) {
		a.saveGeometryEdits()
		return
	}

	if rl.IsKeyPressed(rl.KeyB) {
		a.geometryTool = geometryToolRaiseLower
	}
	if rl.IsKeyPressed(rl.KeyN) {
		a.geometryTool = geometryToolLevel
	}
	if rl.IsKeyPressed(rl.KeyM) {
		a.geometryTool = geometryToolSmooth
	}

	if a.updateGeometryBrushSlider() {
		return
	}

	point, hit := a.geometryAimPoint(a.buildCamera())
	if !hit {
		return
	}

	switch a.geometryTool {
	case geometryToolRaiseLower:
		if rl.IsMouseButtonDown(rl.MouseLeftButton) {
			a.modifyGeometryElevation(point, a.geometryBrushSize, geometryRaiseSpeedMPS*dt)
		} else if rl.IsMouseButtonDown(rl.MouseRightButton) {
			a.modifyGeometryElevation(point, a.geometryBrushSize, -geometryRaiseSpeedMPS*dt)
		}
	case geometryToolLevel:
		if rl.IsMouseButtonDown(rl.MouseRightButton) {
			a.geometryTargetElev = float64(terrainBaseHeightAtLocal(a.terrain, point.X, point.Z)) + a.terrain.centerWorldZ
			a.geometryHasTargetElev = true
		}
		if rl.IsMouseButtonDown(rl.MouseLeftButton) && a.geometryHasTargetElev {
			a.setGeometryElevation(point, a.geometryBrushSize, a.geometryTargetElev)
		}
	case geometryToolSmooth:
		if rl.IsMouseButtonDown(rl.MouseLeftButton) {
			a.smoothGeometryElevation(point, a.geometryBrushSize, geometrySmoothRate*dt)
		}
	}
}

func (a *App) updateGeometryBrushSlider() bool {
	if !rl.IsMouseButtonDown(rl.MouseLeftButton) {
		return false
	}
	mouse := rl.GetMousePosition()
	if !pointInRect(mouse, geometrySliderX-geometrySliderGrab, geometrySliderY-geometrySliderGrab, geometrySliderW+geometrySliderGrab*2, geometrySliderH+geometrySliderGrab*2) {
		return false
	}
	t := (mouse.X - float32(geometrySliderX)) / float32(geometrySliderW)
	t = clamp32(t, 0, 1)
	a.geometryBrushSize = geometryBrushMinSize * float32(math.Pow(float64(geometryBrushMaxSize/geometryBrushMinSize), float64(t)))
	return true
}

func (a *App) geometryAimPoint(camera rl.Camera) (rl.Vector3, bool) {
	if a.terrain == nil {
		return rl.Vector3{}, false
	}
	mouse := rl.GetMousePosition()
	ray := rl.GetScreenToWorldRay(mouse, camera)
	return raycastTerrainBase(a.terrain, ray)
}

func (a *App) drawGeometryEditor3D() {
	if a.terrain == nil {
		return
	}
	point, hit := a.geometryAimPoint(a.buildCamera())
	if !hit {
		return
	}
	segments := 64
	radius := a.geometryBrushSize
	for i := 0; i < segments; i++ {
		a0 := float32(i) / float32(segments) * math.Pi * 2
		a1 := float32(i+1) / float32(segments) * math.Pi * 2
		p0 := rl.NewVector3(
			point.X+float32(math.Cos(float64(a0)))*radius,
			0,
			point.Z+float32(math.Sin(float64(a0)))*radius,
		)
		p1 := rl.NewVector3(
			point.X+float32(math.Cos(float64(a1)))*radius,
			0,
			point.Z+float32(math.Sin(float64(a1)))*radius,
		)
		p0.Y = terrainBaseHeightAtLocal(a.terrain, p0.X, p0.Z) + 0.12
		p1.Y = terrainBaseHeightAtLocal(a.terrain, p1.X, p1.Z) + 0.12
		rl.DrawLine3D(p0, p1, rl.Red)
	}
}

func (a *App) drawGeometryEditorHUD(w, h int32) {
	dirty := ""
	if a.geometryDirty {
		dirty = " *"
	}
	lines := []string{
		fmt.Sprintf("Geometry: %s%s", a.geometryToolName(), dirty),
		fmt.Sprintf("Brush: %.1f m", a.geometryBrushSize),
	}
	if a.geometryTool == geometryToolLevel {
		if a.geometryHasTargetElev {
			lines = append(lines, fmt.Sprintf("Target: %.2f m", a.geometryTargetElev))
		} else {
			lines = append(lines, "Target: right-click to sample")
		}
	}
	if a.geometryStatus != "" && rl.GetTime() < a.geometryStatusUntil {
		lines = append(lines, a.geometryStatus)
	}

	x := geometryPanelX
	y := geometryPanelY
	width := int32(360)
	height := int32(len(lines))*18 + 70
	rl.DrawRectangle(x-6, y-6, width, height, rl.NewColor(25, 25, 25, 210))
	rl.DrawRectangleLines(x-6, y-6, width, height, rl.NewColor(120, 120, 120, 220))
	for i, line := range lines {
		rl.DrawText(line, x, y+int32(i)*18, 14, rl.White)
	}

	sliderY := geometrySliderY
	rl.DrawRectangle(geometrySliderX, sliderY, geometrySliderW, geometrySliderH, rl.DarkGray)
	rl.DrawRectangleLines(geometrySliderX, sliderY, geometrySliderW, geometrySliderH, rl.LightGray)
	norm := float32(math.Log(float64(a.geometryBrushSize/geometryBrushMinSize)) / math.Log(float64(geometryBrushMaxSize/geometryBrushMinSize)))
	norm = clamp32(norm, 0, 1)
	knobX := geometrySliderX + int32(norm*float32(geometrySliderW))
	rl.DrawCircle(knobX, sliderY+geometrySliderH/2, 8, rl.Red)

	if !a.mouseCaptured {
		msg := "MOUSE RELEASED - MMB drag to rotate"
		rl.DrawText(msg, w/2-int32(rl.MeasureText(msg, 16))/2, 8, 16, rl.Orange)
	}
}

func (a *App) geometryToolName() string {
	switch a.geometryTool {
	case geometryToolLevel:
		return "level"
	case geometryToolSmooth:
		return "smooth"
	default:
		return "raise/lower"
	}
}

func (a *App) markGeometryTilesForRebuild(center rl.Vector3, radius float32) {
	t := a.terrain
	if t == nil {
		return
	}
	for _, tile := range t.tiles {
		tx0 := float32(tile.worldWest - t.centerWorldX)
		tx1 := float32(tile.worldEast - t.centerWorldX)
		tz0 := float32(t.centerWorldY - tile.worldNorth)
		tz1 := float32(t.centerWorldY - tile.worldSouth)

		if center.X+radius >= tx0 && center.X-radius <= tx1 &&
			center.Z+radius >= tz0 && center.Z-radius <= tz1 {
			tile.needsRebuild = true
		}
	}
}

func (a *App) modifyGeometryElevation(center rl.Vector3, radius, amount float32) {
	a.editGeometrySamples(center, radius, func(old float64, _ int, _ int) float64 {
		return old + float64(amount)
	})
}

func (a *App) setGeometryElevation(center rl.Vector3, radius float32, target float64) {
	a.editGeometrySamples(center, radius, func(float64, int, int) float64 {
		return target
	})
}

func (a *App) smoothGeometryElevation(center rl.Vector3, radius, intensity float32) {
	if a.terrain == nil {
		return
	}
	t := a.terrain
	x0, x1, z0, z1, ok := geometrySampleBounds(t, center, radius)
	if !ok {
		return
	}

	width := x1 - x0 + 1
	temp := make([]float64, width*(z1-z0+1))
	for z := z0; z <= z1; z++ {
		for x := x0; x <= x1; x++ {
			temp[(z-z0)*width+(x-x0)] = t.heightSamples[z*t.meshWidth+x]
		}
	}

	changed := false
	for z := z0; z <= z1; z++ {
		for x := x0; x <= x1; x++ {
			sx, sz := geometrySampleLocalXZ(t, x, z)
			dx := sx - center.X
			dz := sz - center.Z
			if dx*dx+dz*dz > radius*radius {
				continue
			}
			sum := 0.0
			count := 0.0
			for nz := z - 1; nz <= z+1; nz++ {
				for nx := x - 1; nx <= x+1; nx++ {
					if nx < x0 || nx > x1 || nz < z0 || nz > z1 {
						continue
					}
					sum += temp[(nz-z0)*width+(nx-x0)]
					count++
				}
			}
			if count == 0 {
				continue
			}
			idx := z*t.meshWidth + x
			old := t.heightSamples[idx]
			next := old + (sum/count-old)*float64(intensity)
			if next != old {
				t.heightSamples[idx] = next
				changed = true
			}
		}
	}
	if changed {
		a.geometryDirty = true
		a.updateGeometryHeightRange()
		a.markGeometryTilesForRebuild(center, radius)
	}
}

func (a *App) editGeometrySamples(center rl.Vector3, radius float32, edit func(old float64, x, z int) float64) {
	if a.terrain == nil {
		return
	}
	t := a.terrain
	x0, x1, z0, z1, ok := geometrySampleBounds(t, center, radius)
	if !ok {
		return
	}

	changed := false
	for z := z0; z <= z1; z++ {
		for x := x0; x <= x1; x++ {
			sx, sz := geometrySampleLocalXZ(t, x, z)
			dx := sx - center.X
			dz := sz - center.Z
			if dx*dx+dz*dz > radius*radius {
				continue
			}
			idx := z*t.meshWidth + x
			old := t.heightSamples[idx]
			next := edit(old, x, z)
			if next != old {
				t.heightSamples[idx] = next
				changed = true
			}
		}
	}
	if changed {
		a.geometryDirty = true
		a.updateGeometryHeightRange()
		a.markGeometryTilesForRebuild(center, radius)
	}
}

func geometrySampleBounds(t *terrainData, center rl.Vector3, radius float32) (int, int, int, int, bool) {
	if t == nil || t.meshWidth < 2 || t.meshHeight < 2 || radius <= 0 {
		return 0, 0, 0, 0, false
	}
	fx := float64(center.X-t.position.X) / float64(t.widthMeters) * float64(t.meshWidth-1)
	fz := float64(center.Z-t.position.Z) / float64(t.depthMeters) * float64(t.meshHeight-1)
	rx := float64(radius) / float64(t.widthMeters) * float64(t.meshWidth-1)
	rz := float64(radius) / float64(t.depthMeters) * float64(t.meshHeight-1)

	x0 := int(math.Floor(fx - rx))
	x1 := int(math.Ceil(fx + rx))
	z0 := int(math.Floor(fz - rz))
	z1 := int(math.Ceil(fz + rz))

	x0 = max(0, min(x0, t.meshWidth-1))
	x1 = max(0, min(x1, t.meshWidth-1))
	z0 = max(0, min(z0, t.meshHeight-1))
	z1 = max(0, min(z1, t.meshHeight-1))
	return x0, x1, z0, z1, x0 <= x1 && z0 <= z1
}

func geometrySampleLocalXZ(t *terrainData, x, z int) (float32, float32) {
	localX := t.position.X + float32(x)/float32(t.meshWidth-1)*t.widthMeters
	localZ := t.position.Z + float32(z)/float32(t.meshHeight-1)*t.depthMeters
	return localX, localZ
}

func (a *App) updateGeometryHeightRange() {
	t := a.terrain
	if t == nil || len(t.heightSamples) == 0 {
		return
	}
	maxHeight := t.heightSamples[0]
	for _, h := range t.heightSamples[1:] {
		if h > maxHeight {
			maxHeight = h
		}
	}
	t.heightMax = maxHeight
	if t.heightMax < t.heightMin {
		t.heightMax = t.heightMin
	}
	t.heightMeters = float32(t.heightMax - t.heightMin)
	if t.heightMeters < 1 {
		t.heightMeters = 1
	}
}

func (a *App) pumpGeometryTileRebuilds() {
	t := a.terrain
	if t == nil || len(t.tiles) == 0 {
		return
	}
	for i := 0; i < len(t.tiles); i++ {
		t.rebuildIdx = (t.rebuildIdx + 1) % len(t.tiles)
		tile := t.tiles[t.rebuildIdx]
		if tile.needsRebuild {
			tile.rebuildMesh(t)
			return
		}
	}
}

func (a *App) saveGeometryEdits() {
	if a.terrain == nil || a.mapDef == nil {
		return
	}
	if len(a.mapDef.DEMTiles) == 0 {
		a.setGeometryStatus("save failed: map has no DEM tiles")
		return
	}

	saved := 0
	for _, tile := range a.mapDef.DEMTiles {
		meta, err := readDEMMetadata(tile.Path)
		if err != nil {
			fmt.Printf("Failed to save DEM %s: %v\n", tile.Path, err)
			continue
		}
		west, _, _, north := demWorldBounds(meta)
		if err := writeASC(tile.Path, meta, func(row, col int) float64 {
			worldX := west + (float64(col)+0.5)*meta.cellSize
			worldY := north - (float64(row)+0.5)*meta.cellSize
			localX := float32(worldX - a.terrain.centerWorldX)
			localZ := float32(a.terrain.centerWorldY - worldY)
			return float64(terrainBaseHeightAtLocal(a.terrain, localX, localZ)) + a.terrain.centerWorldZ
		}); err != nil {
			fmt.Printf("Failed to write DEM %s: %v\n", tile.Path, err)
			continue
		}
		fmt.Printf("Saved DEM %s\n", tile.Path)
		saved++
	}
	if saved == 0 {
		a.setGeometryStatus("save failed")
		return
	}
	a.geometryDirty = false
	a.setGeometryStatus(fmt.Sprintf("saved %d DEM tiles", saved))
}

func (a *App) setGeometryStatus(msg string) {
	a.geometryStatus = msg
	if msg == "" {
		a.geometryStatusUntil = 0
		return
	}
	a.geometryStatusUntil = rl.GetTime() + 4
}
