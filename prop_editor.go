package main

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"

	rl "github.com/gen2brain/raylib-go/raylib"
)

type propEditTool int

const (
	propEditToolPlace propEditTool = iota
	propEditToolSelect
	propEditToolLinear
)

const (
	propEditorPanelX       = int32(8)
	propEditorPanelY       = int32(34)
	propEditorPanelWidth   = int32(600)
	propEditorLineHeight   = int32(18)
	propEditorAssetRowH    = int32(20)
	propEditorAssetListY   = int32(154)
	propEditorAssetListMax = 8
)

func (a *App) togglePropEditor() {
	if a.editMode {
		a.editMode = false
		a.draggingProp = false
		a.mouseCaptured = true
		rl.DisableCursor()
		a.setPropStatus("")
		return
	}
	if a.terrain == nil || a.objects == nil || a.mapDef == nil {
		a.setPropStatus("load a map before editing props")
		return
	}
	a.editMode = true
	a.propTool = propEditToolPlace
	a.mouseCaptured = false
	a.paused = true
	rl.EnableCursor()
	if a.currentPropAsset == "" {
		a.currentPropAsset = defaultPropAsset
	}
	if a.currentPropScale == 0 {
		a.currentPropScale = 1
	}
	if a.currentLinearAsset == "" {
		a.currentLinearAsset = defaultLinearPropAsset
	}
	if a.currentLinearScale == 0 {
		a.currentLinearScale = 1
	}
	if a.currentLinearSpacing <= 0 {
		a.currentLinearSpacing = defaultLinearSpacingM
	}
	a.refreshAvailablePropAssets()
	if a.objects.PropAssets == nil {
		a.objects.PropAssets = map[string]*propAsset{}
	}
	if _, err := ensurePropAssetLoaded(a.mapDef, a.objects.PropAssets, a.activeEditorAsset()); err != nil {
		a.setPropStatus(err.Error())
	}
}

func (a *App) updatePropEditor() {
	if a.terrain == nil || a.objects == nil {
		return
	}

	ctrlDown := rl.IsKeyDown(rl.KeyLeftControl) || rl.IsKeyDown(rl.KeyRightControl)
	if ctrlDown && rl.IsKeyPressed(rl.KeyS) {
		a.savePropsFromEditor()
		return
	}

	if rl.IsKeyPressed(rl.KeyOne) {
		a.propTool = propEditToolPlace
		a.draggingProp = false
		a.selectedLinearProp = -1
	}
	if rl.IsKeyPressed(rl.KeyTwo) {
		a.propTool = propEditToolSelect
		a.draggingProp = false
	}
	if rl.IsKeyPressed(rl.KeyThree) {
		a.propTool = propEditToolLinear
		a.draggingProp = false
		a.selectedProp = -1
		if a.objects.PropAssets == nil {
			a.objects.PropAssets = map[string]*propAsset{}
		}
		if _, err := ensurePropAssetLoaded(a.mapDef, a.objects.PropAssets, a.currentLinearAsset); err != nil {
			a.setPropStatus(err.Error())
		}
	}

	if rl.IsKeyPressed(rl.KeyDelete) || rl.IsKeyPressed(rl.KeyBackspace) {
		if a.propTool == propEditToolLinear && len(a.linearDraft) > 0 {
			a.linearDraft = a.linearDraft[:len(a.linearDraft)-1]
		} else {
			a.deleteSelectedEditorItem()
		}
	}

	if rl.IsKeyPressed(rl.KeyEnter) && a.propTool == propEditToolLinear {
		a.commitLinearDraft()
	}
	if rl.IsKeyPressed(rl.KeyC) && a.propTool == propEditToolLinear {
		a.linearDraft = nil
		a.setPropStatus("linear draft cleared")
	}

	a.updatePropTransformKeys()
	a.updatePropMouseRotation()

	if rl.IsMouseButtonPressed(rl.MouseLeftButton) && a.handleAssetPickerClick() {
		return
	}

	camera := a.buildCamera()
	hitPoint, hitTerrain := a.mouseTerrainPoint(camera)
	switch a.propTool {
	case propEditToolPlace:
		if hitTerrain && rl.IsMouseButtonPressed(rl.MouseLeftButton) {
			a.addPropAt(hitPoint)
		}
	case propEditToolSelect:
		if rl.IsMouseButtonPressed(rl.MouseLeftButton) {
			idx := a.pickProp(camera)
			if idx >= 0 {
				a.selectedProp = idx
				a.selectedLinearProp = -1
				a.draggingProp = true
			} else {
				lineIdx := a.pickLinearProp(camera)
				a.selectedProp = -1
				a.selectedLinearProp = lineIdx
				a.draggingProp = false
			}
		}
		if a.draggingProp && rl.IsMouseButtonDown(rl.MouseLeftButton) && hitTerrain {
			a.moveSelectedProp(hitPoint)
		}
		if rl.IsMouseButtonReleased(rl.MouseLeftButton) {
			a.draggingProp = false
		}
	case propEditToolLinear:
		if hitTerrain && rl.IsMouseButtonPressed(rl.MouseLeftButton) {
			a.addLinearDraftPoint(hitPoint)
		}
	}
}

func (a *App) refreshAvailablePropAssets() {
	props := []propInstance(nil)
	linear := []linearPropInstance(nil)
	if a.objects != nil {
		props = a.objects.Props
		linear = a.objects.LinearProps
	}
	a.availablePropAssets = discoverPropAssets(a.mapDef, props, linear)
	if len(a.availablePropAssets) == 0 {
		a.availablePropAssets = []string{defaultPropAsset}
	}
	hasPropAsset := false
	hasLinearAsset := false
	for _, asset := range a.availablePropAssets {
		if asset == a.currentPropAsset {
			hasPropAsset = true
		}
		if asset == a.currentLinearAsset {
			hasLinearAsset = true
		}
	}
	if !hasPropAsset {
		a.currentPropAsset = a.availablePropAssets[0]
	}
	if !hasLinearAsset {
		a.currentLinearAsset = defaultLinearPropAsset
		for _, asset := range a.availablePropAssets {
			if asset == defaultLinearPropAsset {
				return
			}
		}
		a.currentLinearAsset = a.availablePropAssets[0]
	}
}

func (a *App) updatePropTransformKeys() {
	step := float32(15)
	if rl.IsKeyDown(rl.KeyLeftShift) || rl.IsKeyDown(rl.KeyRightShift) {
		step = 5
	}

	rotateDelta := float32(0)
	if rl.IsKeyPressed(rl.KeyLeftBracket) {
		rotateDelta -= step
	}
	if rl.IsKeyPressed(rl.KeyRightBracket) || rl.IsKeyPressed(rl.KeyR) {
		rotateDelta += step
	}
	if rotateDelta != 0 {
		if a.propTool == propEditToolLinear {
			if item := a.selectedLinearInstance(); item != nil {
				item.HeadingOffsetDeg = normalizeDegrees(item.HeadingOffsetDeg + rotateDelta)
				a.propDirty = true
			} else {
				a.currentLinearHeadingOffset = normalizeDegrees(a.currentLinearHeadingOffset + rotateDelta)
			}
		} else if prop := a.selectedPropInstance(); prop != nil {
			prop.HeadingDeg = normalizeDegrees(prop.HeadingDeg + rotateDelta)
			a.propDirty = true
		} else {
			a.currentPropHeading = normalizeDegrees(a.currentPropHeading + rotateDelta)
		}
	}

	scaleMul := float32(1)
	if rl.IsKeyPressed(rl.KeyEqual) {
		scaleMul = 1.1
	}
	if rl.IsKeyPressed(rl.KeyMinus) {
		scaleMul = 1 / 1.1
	}
	if scaleMul != 1 {
		if a.propTool == propEditToolLinear {
			if item := a.selectedLinearInstance(); item != nil {
				item.Scale = clamp32(item.Scale*scaleMul, 0.05, 20)
				a.propDirty = true
			} else {
				a.currentLinearScale = clamp32(a.currentLinearScale*scaleMul, 0.05, 20)
			}
		} else if prop := a.selectedPropInstance(); prop != nil {
			prop.Scale = clamp32(prop.Scale*scaleMul, 0.05, 20)
			a.propDirty = true
		} else {
			a.currentPropScale = clamp32(a.currentPropScale*scaleMul, 0.05, 20)
		}
	}

	if a.propTool == propEditToolLinear {
		if rl.IsKeyPressed(rl.KeyComma) {
			a.currentLinearSpacing = clamp32(a.currentLinearSpacing-0.25, 0.25, 100)
		}
		if rl.IsKeyPressed(rl.KeyPeriod) {
			a.currentLinearSpacing = clamp32(a.currentLinearSpacing+0.25, 0.25, 100)
		}
	}
}

func (a *App) updatePropMouseRotation() {
	if !rl.IsMouseButtonDown(rl.MouseMiddleButton) {
		return
	}
	delta := rl.GetMouseDelta()
	if delta.X == 0 {
		return
	}
	rotateDelta := delta.X * 0.35
	if a.propTool == propEditToolLinear {
		if item := a.selectedLinearInstance(); item != nil {
			item.HeadingOffsetDeg = normalizeDegrees(item.HeadingOffsetDeg + rotateDelta)
			a.propDirty = true
			return
		}
		a.currentLinearHeadingOffset = normalizeDegrees(a.currentLinearHeadingOffset + rotateDelta)
		return
	}
	if a.propTool == propEditToolSelect {
		prop := a.selectedPropInstance()
		if prop == nil {
			return
		}
		prop.HeadingDeg = normalizeDegrees(prop.HeadingDeg + rotateDelta)
		a.propDirty = true
		return
	}
	a.currentPropHeading = normalizeDegrees(a.currentPropHeading + rotateDelta)
}

func (a *App) handleAssetPickerClick() bool {
	mouse := rl.GetMousePosition()
	if !pointInRect(mouse, propEditorPanelX-4, propEditorPanelY-6, propEditorPanelWidth, a.propEditorPanelHeight()) {
		return false
	}
	if int32(mouse.Y) >= propEditorAssetListY {
		row := int((int32(mouse.Y) - propEditorAssetListY) / propEditorAssetRowH)
		if row >= 0 && row < len(a.availablePropAssets) && row < propEditorAssetListMax {
			asset := a.availablePropAssets[row]
			if a.propTool == propEditToolLinear {
				a.currentLinearAsset = asset
			} else {
				a.currentPropAsset = asset
			}
			if a.objects != nil {
				if a.objects.PropAssets == nil {
					a.objects.PropAssets = map[string]*propAsset{}
				}
				if _, err := ensurePropAssetLoaded(a.mapDef, a.objects.PropAssets, asset); err != nil {
					a.setPropStatus(err.Error())
				} else {
					a.setPropStatus(fmt.Sprintf("selected %s", filepath.Base(asset)))
				}
			}
		}
	}
	return true
}

func (a *App) activeEditorAsset() string {
	if a.propTool == propEditToolLinear {
		return a.currentLinearAsset
	}
	return a.currentPropAsset
}

func (a *App) addPropAt(point rl.Vector3) {
	if a.objects == nil || a.terrain == nil {
		return
	}
	if a.objects.PropAssets == nil {
		a.objects.PropAssets = map[string]*propAsset{}
	}
	if _, err := ensurePropAssetLoaded(a.mapDef, a.objects.PropAssets, a.currentPropAsset); err != nil {
		a.setPropStatus(err.Error())
		return
	}
	worldX, worldY := localXZToWorld(a.terrain, point.X, point.Z)
	scale := a.currentPropScale
	if scale == 0 {
		scale = 1
	}
	prop := propInstance{
		ID:         a.nextPropID(),
		Asset:      a.currentPropAsset,
		WorldX:     worldX,
		WorldY:     worldY,
		WorldZ:     nil,
		HeadingDeg: normalizeDegrees(a.currentPropHeading),
		Scale:      scale,
		Category:   propCategoryFromAsset(a.currentPropAsset),
		Tags:       []string{"static"},
		SourcePath: primaryPropLayerPath(a.mapDef),
	}
	a.objects.Props = append(a.objects.Props, prop)
	a.selectedProp = len(a.objects.Props) - 1
	a.propDirty = true
	a.setPropStatus(fmt.Sprintf("placed %s", prop.ID))
}

func (a *App) moveSelectedProp(point rl.Vector3) {
	prop := a.selectedPropInstance()
	if prop == nil || a.terrain == nil {
		return
	}
	worldX, worldY := localXZToWorld(a.terrain, point.X, point.Z)
	prop.WorldX = worldX
	prop.WorldY = worldY
	prop.WorldZ = nil
	prop.SourcePath = primaryPropLayerPath(a.mapDef)
	a.propDirty = true
}

func (a *App) addLinearDraftPoint(point rl.Vector3) {
	if a.terrain == nil {
		return
	}
	worldX, worldY := localXZToWorld(a.terrain, point.X, point.Z)
	a.linearDraft = append(a.linearDraft, linearPropPoint{WorldX: worldX, WorldY: worldY})
	a.selectedLinearProp = -1
	a.selectedProp = -1
	a.setPropStatus(fmt.Sprintf("linear points: %d", len(a.linearDraft)))
}

func (a *App) commitLinearDraft() {
	if a.objects == nil || len(a.linearDraft) < 2 {
		a.setPropStatus("linear draft needs at least two points")
		return
	}
	if a.objects.PropAssets == nil {
		a.objects.PropAssets = map[string]*propAsset{}
	}
	if _, err := ensurePropAssetLoaded(a.mapDef, a.objects.PropAssets, a.currentLinearAsset); err != nil {
		a.setPropStatus(err.Error())
		return
	}
	scale := a.currentLinearScale
	if scale == 0 {
		scale = 1
	}
	spacing := a.currentLinearSpacing
	if spacing <= 0 {
		spacing = defaultLinearSpacingM
	}
	item := linearPropInstance{
		ID:               a.nextLinearPropID(),
		Asset:            a.currentLinearAsset,
		Points:           append([]linearPropPoint(nil), a.linearDraft...),
		SpacingM:         spacing,
		Scale:            scale,
		HeadingOffsetDeg: normalizeDegrees(a.currentLinearHeadingOffset),
		Category:         propCategoryFromAsset(a.currentLinearAsset),
		Tags:             []string{"static"},
		SourcePath:       primaryPropLayerPath(a.mapDef),
	}
	a.objects.LinearProps = append(a.objects.LinearProps, item)
	a.selectedLinearProp = len(a.objects.LinearProps) - 1
	a.selectedProp = -1
	a.linearDraft = nil
	a.propDirty = true
	a.setPropStatus(fmt.Sprintf("placed %s", item.ID))
}

func (a *App) deleteSelectedProp() {
	if a.objects == nil || a.selectedProp < 0 || a.selectedProp >= len(a.objects.Props) {
		return
	}
	deletedID := a.objects.Props[a.selectedProp].ID
	a.objects.Props = append(a.objects.Props[:a.selectedProp], a.objects.Props[a.selectedProp+1:]...)
	if a.selectedProp >= len(a.objects.Props) {
		a.selectedProp = len(a.objects.Props) - 1
	}
	a.draggingProp = false
	a.propDirty = true
	a.setPropStatus(fmt.Sprintf("deleted %s", deletedID))
}

func (a *App) deleteSelectedEditorItem() {
	if a.selectedLinearProp >= 0 {
		a.deleteSelectedLinearProp()
		return
	}
	a.deleteSelectedProp()
}

func (a *App) deleteSelectedLinearProp() {
	if a.objects == nil || a.selectedLinearProp < 0 || a.selectedLinearProp >= len(a.objects.LinearProps) {
		return
	}
	deletedID := a.objects.LinearProps[a.selectedLinearProp].ID
	a.objects.LinearProps = append(a.objects.LinearProps[:a.selectedLinearProp], a.objects.LinearProps[a.selectedLinearProp+1:]...)
	if a.selectedLinearProp >= len(a.objects.LinearProps) {
		a.selectedLinearProp = len(a.objects.LinearProps) - 1
	}
	a.propDirty = true
	a.setPropStatus(fmt.Sprintf("deleted %s", deletedID))
}

func (a *App) savePropsFromEditor() {
	if a.objects == nil {
		return
	}
	if err := savePropInstances(a.mapDef, a.objects.Props, a.objects.LinearProps); err != nil {
		a.setPropStatus(err.Error())
		return
	}
	a.propDirty = false
	a.setPropStatus(fmt.Sprintf("saved %d props", len(a.objects.Props)))
}

func (a *App) drawPropEditor3D(camera rl.Camera) {
	if a.terrain == nil || a.objects == nil {
		return
	}

	if a.selectedProp >= 0 && a.selectedProp < len(a.objects.Props) {
		prop := &a.objects.Props[a.selectedProp]
		asset := a.objects.PropAssets[prop.Asset]
		pos := propSelectionCenter(a.terrain, prop, asset)
		radius := a.propRadius(prop)
		rl.DrawSphereWires(pos, radius, 12, 12, rl.Yellow)
	}
	if a.selectedLinearProp >= 0 && a.selectedLinearProp < len(a.objects.LinearProps) {
		a.drawLinearSelection(&a.objects.LinearProps[a.selectedLinearProp], rl.Yellow)
	}

	if a.propTool == propEditToolLinear {
		a.drawLinearDraft()
		return
	}
	if a.propTool != propEditToolPlace {
		return
	}
	point, ok := a.mouseTerrainPoint(camera)
	if !ok {
		return
	}
	if a.objects.PropAssets == nil {
		a.objects.PropAssets = map[string]*propAsset{}
	}
	asset, err := ensurePropAssetLoaded(a.mapDef, a.objects.PropAssets, a.currentPropAsset)
	if err != nil || asset == nil || !asset.Loaded {
		return
	}
	scale := a.currentPropScale
	if scale == 0 {
		scale = 1
	}
	drawPos := propDrawPositionFromBase(point, asset, scale)
	rl.DrawModelWiresEx(
		asset.Model,
		drawPos,
		rl.NewVector3(0, 1, 0),
		a.currentPropHeading,
		rl.NewVector3(scale, scale, scale),
		rl.Lime,
	)
	center := drawPos
	center.Y += (asset.Bounds.Min.Y + asset.Bounds.Max.Y) * 0.5 * scale
	rl.DrawSphereWires(center, asset.Radius*scale, 8, 8, rl.NewColor(40, 220, 80, 180))
}

func (a *App) drawLinearDraft() {
	if a.terrain == nil || len(a.linearDraft) == 0 {
		return
	}
	if a.objects != nil {
		if a.objects.PropAssets == nil {
			a.objects.PropAssets = map[string]*propAsset{}
		}
		_, _ = ensurePropAssetLoaded(a.mapDef, a.objects.PropAssets, a.currentLinearAsset)
	}
	for i, point := range a.linearDraft {
		pos := linearPointLocalPosition(a.terrain, point)
		rl.DrawSphere(pos, 0.8, rl.Lime)
		if i > 0 {
			prev := linearPointLocalPosition(a.terrain, a.linearDraft[i-1])
			prev.Y += 0.08
			pos.Y += 0.08
			rl.DrawLine3D(prev, pos, rl.Lime)
		}
	}
	if len(a.linearDraft) >= 2 {
		item := linearPropInstance{
			Asset:            a.currentLinearAsset,
			Points:           a.linearDraft,
			SpacingM:         a.currentLinearSpacing,
			Scale:            a.currentLinearScale,
			HeadingOffsetDeg: a.currentLinearHeadingOffset,
		}
		drawLinearProp(a.terrain, a.objects, &item)
	}
}

func (a *App) drawLinearSelection(item *linearPropInstance, color rl.Color) {
	if a.terrain == nil || item == nil || len(item.Points) == 0 {
		return
	}
	for i, point := range item.Points {
		pos := linearPointLocalPosition(a.terrain, point)
		rl.DrawSphereWires(pos, 1.1, 8, 8, color)
		if i > 0 {
			prev := linearPointLocalPosition(a.terrain, item.Points[i-1])
			prev.Y += 0.12
			pos.Y += 0.12
			rl.DrawLine3D(prev, pos, color)
		}
	}
}

func (a *App) drawPropEditorHUD(w, h int32) {
	_ = h
	a.refreshAvailablePropAssets()
	tool := "place"
	if a.propTool == propEditToolSelect {
		tool = "select/move"
	} else if a.propTool == propEditToolLinear {
		tool = "linear"
	}
	dirty := ""
	if a.propDirty {
		dirty = "  unsaved"
	}
	selected := "none"
	if prop := a.selectedPropInstance(); prop != nil {
		selected = prop.ID
	} else if item := a.selectedLinearInstance(); item != nil {
		selected = item.ID
	}
	layer := "none"
	if a.mapDef != nil {
		layer = filepath.ToSlash(primaryPropLayerPath(a.mapDef))
	}

	lines := []string{
		fmt.Sprintf("Prop editor: %s%s", tool, dirty),
		fmt.Sprintf("asset: %s", a.activeEditorAsset()),
		fmt.Sprintf("selected: %s", selected),
		fmt.Sprintf("layer: %s", layer),
		"assets:",
	}
	if a.propStatus != "" && rl.GetTime() < a.propStatusUntil {
		lines = append(lines, a.propStatus)
	}
	if a.propTool == propEditToolLinear {
		lines = append(lines, fmt.Sprintf("linear spacing: %.2fm  scale: %.2f  offset: %.0f deg  draft points: %d",
			a.currentLinearSpacing, a.currentLinearScale, a.currentLinearHeadingOffset, len(a.linearDraft)))
	}

	x := propEditorPanelX
	y := propEditorPanelY
	width := propEditorPanelWidth
	lineH := propEditorLineHeight
	height := a.propEditorPanelHeight()
	if width > w-16 {
		width = w - 16
	}
	rl.DrawRectangle(x-4, y-6, width, height, rl.NewColor(0, 0, 0, 155))
	for i, line := range lines {
		rl.DrawText(line, x, y+int32(i)*lineH, 14, rl.White)
	}
	a.drawAssetPicker(x, width)
}

func (a *App) drawAssetPicker(x, width int32) {
	mouse := rl.GetMousePosition()
	rowW := width - 16
	if rowW < 160 {
		rowW = 160
	}
	count := len(a.availablePropAssets)
	if count > propEditorAssetListMax {
		count = propEditorAssetListMax
	}
	for i := 0; i < count; i++ {
		y := propEditorAssetListY + int32(i)*propEditorAssetRowH
		asset := a.availablePropAssets[i]
		active := a.activeEditorAsset()
		bg := rl.NewColor(35, 35, 35, 190)
		if asset == active {
			bg = rl.NewColor(64, 92, 52, 220)
		} else if pointInRect(mouse, x, y, rowW, propEditorAssetRowH-2) {
			bg = rl.NewColor(55, 55, 55, 210)
		}
		rl.DrawRectangle(x, y, rowW, propEditorAssetRowH-2, bg)
		rl.DrawText(filepath.Base(asset), x+6, y+3, 13, rl.White)
	}
}

func (a *App) propEditorPanelHeight() int32 {
	rows := len(a.availablePropAssets)
	if rows > propEditorAssetListMax {
		rows = propEditorAssetListMax
	}
	if rows < 1 {
		rows = 1
	}
	return propEditorAssetListY - propEditorPanelY + int32(rows)*propEditorAssetRowH + 10
}

func (a *App) mouseTerrainPoint(camera rl.Camera) (rl.Vector3, bool) {
	mouse := rl.GetMousePosition()
	ray := rl.GetScreenToWorldRay(mouse, camera)
	return raycastTerrain(a.terrain, ray)
}

func (a *App) pickProp(camera rl.Camera) int {
	if a.terrain == nil || a.objects == nil {
		return -1
	}
	mouse := rl.GetMousePosition()
	ray := rl.GetScreenToWorldRay(mouse, camera)
	bestIdx := -1
	bestDist := float32(math.MaxFloat32)
	for i := range a.objects.Props {
		prop := &a.objects.Props[i]
		asset := a.objects.PropAssets[prop.Asset]
		pos := propSelectionCenter(a.terrain, prop, asset)
		radius := a.propRadius(prop)
		hit := rl.GetRayCollisionSphere(ray, pos, radius)
		if hit.Hit && hit.Distance < bestDist {
			bestDist = hit.Distance
			bestIdx = i
		}
	}
	return bestIdx
}

func (a *App) pickLinearProp(camera rl.Camera) int {
	if a.terrain == nil || a.objects == nil {
		return -1
	}
	mouse := rl.GetMousePosition()
	ray := rl.GetScreenToWorldRay(mouse, camera)
	bestIdx := -1
	bestDist := float32(math.MaxFloat32)
	for i := range a.objects.LinearProps {
		item := &a.objects.LinearProps[i]
		for _, point := range item.Points {
			pos := linearPointLocalPosition(a.terrain, point)
			hit := rl.GetRayCollisionSphere(ray, pos, 2.5)
			if hit.Hit && hit.Distance < bestDist {
				bestDist = hit.Distance
				bestIdx = i
			}
		}
	}
	return bestIdx
}

func (a *App) propRadius(prop *propInstance) float32 {
	radius := float32(2)
	if a.objects != nil && prop != nil {
		if asset := a.objects.PropAssets[prop.Asset]; asset != nil && asset.Radius > 0 {
			radius = asset.Radius
		}
	}
	scale := float32(1)
	if prop != nil && prop.Scale > 0 {
		scale = prop.Scale
	}
	return radius * scale
}

func (a *App) selectedPropInstance() *propInstance {
	if a.objects == nil || a.selectedProp < 0 || a.selectedProp >= len(a.objects.Props) {
		return nil
	}
	return &a.objects.Props[a.selectedProp]
}

func (a *App) selectedLinearInstance() *linearPropInstance {
	if a.objects == nil || a.selectedLinearProp < 0 || a.selectedLinearProp >= len(a.objects.LinearProps) {
		return nil
	}
	return &a.objects.LinearProps[a.selectedLinearProp]
}

func (a *App) nextPropID() string {
	used := map[string]bool{}
	if a.objects != nil {
		for _, prop := range a.objects.Props {
			used[prop.ID] = true
		}
	}
	for i := 1; ; i++ {
		id := fmt.Sprintf("prop_%04d", i)
		if !used[id] {
			return id
		}
	}
}

func (a *App) nextLinearPropID() string {
	used := map[string]bool{}
	if a.objects != nil {
		for _, item := range a.objects.LinearProps {
			used[item.ID] = true
		}
	}
	for i := 1; ; i++ {
		id := fmt.Sprintf("linear_%04d", i)
		if !used[id] {
			return id
		}
	}
}

func (a *App) setPropStatus(msg string) {
	a.propStatus = msg
	if msg == "" {
		a.propStatusUntil = 0
		return
	}
	a.propStatusUntil = rl.GetTime() + 4
}

func propCategoryFromAsset(asset string) string {
	base := strings.ToLower(filepath.ToSlash(asset))
	switch {
	case strings.Contains(base, "car"):
		return "parked_car"
	case strings.Contains(base, "sign"):
		return "traffic_sign"
	case strings.Contains(base, "fence"):
		return "fence"
	default:
		return "prop"
	}
}

func raycastTerrain(t *terrainData, ray rl.Ray) (rl.Vector3, bool) {
	if t == nil {
		return rl.Vector3{}, false
	}
	const (
		maxDist    = float32(5000)
		stepMeters = float32(8)
	)

	prevD := float32(0)
	prevDiff := float32(0)
	prevValid := false

	for d := float32(0); d <= maxDist; d += stepMeters {
		p := pointOnRay(ray, d)
		if !terrainContainsLocalXZ(t, p.X, p.Z) {
			prevValid = false
			continue
		}
		ground := terrainHeightAtLocal(t, p.X, p.Z)
		diff := p.Y - ground
		if diff <= 0 {
			if !prevValid {
				p.Y = ground
				return p, true
			}
			lo := prevD
			hi := d
			for i := 0; i < 18; i++ {
				mid := (lo + hi) * 0.5
				mp := pointOnRay(ray, mid)
				mdiff := mp.Y - terrainHeightAtLocal(t, mp.X, mp.Z)
				if mdiff > 0 {
					lo = mid
				} else {
					hi = mid
				}
			}
			p = pointOnRay(ray, hi)
			p.Y = terrainHeightAtLocal(t, p.X, p.Z)
			return p, true
		}
		prevD = d
		prevDiff = diff
		prevValid = prevDiff > 0
	}

	return rl.Vector3{}, false
}

func pointOnRay(ray rl.Ray, dist float32) rl.Vector3 {
	return rl.NewVector3(
		ray.Position.X+ray.Direction.X*dist,
		ray.Position.Y+ray.Direction.Y*dist,
		ray.Position.Z+ray.Direction.Z*dist,
	)
}

func terrainContainsLocalXZ(t *terrainData, localX, localZ float32) bool {
	if t == nil {
		return false
	}
	return localX >= t.position.X &&
		localX <= t.position.X+t.widthMeters &&
		localZ >= t.position.Z &&
		localZ <= t.position.Z+t.depthMeters
}

func pointInRect(point rl.Vector2, x, y, width, height int32) bool {
	return point.X >= float32(x) &&
		point.X <= float32(x+width) &&
		point.Y >= float32(y) &&
		point.Y <= float32(y+height)
}
