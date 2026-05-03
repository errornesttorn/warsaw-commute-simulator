package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"

	rl "github.com/gen2brain/raylib-go/raylib"
)

const (
	roadMaskSnapMeters  = float32(1.2)
	roadMaskHoverMeters = float32(1.2)
	roadMaskNodeRadius  = float32(0.35)
	roadMaskLineRadius  = float32(0.08)
	roadMaskOverlayLift = float32(0.22)
	roadMaskCurveSteps  = 32

	roadMaskDragPreviewInterval = 0.12
)

type roadMaskEditTool int

const (
	roadMaskToolDraw roadMaskEditTool = iota
	roadMaskToolEdit
	roadMaskToolDelete
	roadMaskToolCut
)

type roadMaskCurbKind int

const (
	roadMaskCurbHard roadMaskCurbKind = iota
	roadMaskCurbSoft
)

func (c roadMaskCurbKind) String() string {
	if c == roadMaskCurbSoft {
		return "soft"
	}
	return "hard"
}

type roadMaskVec2 struct {
	X float32
	Z float32
}

func (v roadMaskVec2) Add(u roadMaskVec2) roadMaskVec2 {
	return roadMaskVec2{X: v.X + u.X, Z: v.Z + u.Z}
}

func (v roadMaskVec2) Sub(u roadMaskVec2) roadMaskVec2 {
	return roadMaskVec2{X: v.X - u.X, Z: v.Z - u.Z}
}

func (v roadMaskVec2) Scale(s float32) roadMaskVec2 {
	return roadMaskVec2{X: v.X * s, Z: v.Z * s}
}

func (v roadMaskVec2) Dot(u roadMaskVec2) float32 {
	return v.X*u.X + v.Z*u.Z
}

func (v roadMaskVec2) LenSq() float32 {
	return v.Dot(v)
}

func (v roadMaskVec2) Len() float32 {
	return float32(math.Sqrt(float64(v.LenSq())))
}

func (v roadMaskVec2) Lerp(u roadMaskVec2, t float32) roadMaskVec2 {
	return roadMaskVec2{X: v.X + (u.X-v.X)*t, Z: v.Z + (u.Z-v.Z)*t}
}

func roadMaskDist(a, b roadMaskVec2) float32 {
	return a.Sub(b).Len()
}

func roadMaskDist2(a, b roadMaskVec2) float32 {
	return a.Sub(b).LenSq()
}

type roadMaskEditSegProps struct {
	IsSpline bool
	Curb     roadMaskCurbKind
	MidPt    roadMaskVec2
}

type roadMaskEditNode struct {
	ID  int
	Pos roadMaskVec2
}

type roadMaskEditEdge struct {
	ID      int
	NodeIDs []int
	Segs    []roadMaskEditSegProps
}

type roadMaskEditor struct {
	nodes      []roadMaskEditNode
	edges      []roadMaskEditEdge
	nextNodeID int
	nextEdgeID int

	tool            roadMaskEditTool
	currentCurb     roadMaskCurbKind
	currentIsSpline bool

	drawing           bool
	draftNodeIDs      []int
	draftSegProps     []roadMaskEditSegProps
	splinePickingMid  bool
	splinePendingCurb roadMaskCurbKind

	selNodeID int
	selEdgeID int
	selSegIdx int
	dragging  bool

	path        string
	geo         roadMaskGeoTIFF
	dirty       bool
	status      string
	statusUntil float64
}

func (a *App) toggleRoadMaskEditor() {
	if a.roadMaskMode {
		a.pumpRoadMaskPreviewRebuild(true)
		a.roadMaskMode = false
		a.applyMouseCapture()
		return
	}
	if a.terrain == nil || a.mapDef == nil {
		fmt.Println("load a map before editing road masks")
		return
	}
	if a.roadMaskEditor == nil {
		ed, err := loadRoadMaskEditor(a.mapDef, a.terrain)
		if err != nil {
			fmt.Printf("Failed to load road mask editor: %v\n", err)
			return
		}
		a.roadMaskEditor = ed
	}
	a.roadMaskMode = true
	a.paused = true
	a.applyMouseCapture()
}

func (a *App) updateRoadMaskEditor() {
	if a.terrain == nil || a.mapDef == nil {
		return
	}
	if a.roadMaskEditor == nil {
		ed, err := loadRoadMaskEditor(a.mapDef, a.terrain)
		if err != nil {
			fmt.Printf("Failed to load road mask editor: %v\n", err)
			return
		}
		a.roadMaskEditor = ed
	}
	a.paused = true

	ctrlDown := rl.IsKeyDown(rl.KeyLeftControl) || rl.IsKeyDown(rl.KeyRightControl)
	if ctrlDown && rl.IsKeyPressed(rl.KeyS) {
		a.saveRoadMaskFromEditor()
		return
	}

	pos, ok := a.roadMaskAimPoint(a.buildCamera())
	wasDragging := a.roadMaskEditor.dragging
	changed := a.roadMaskEditor.handleInput(pos, ok)
	dragReleased := wasDragging && !a.roadMaskEditor.dragging
	if dragReleased || (changed && !a.roadMaskEditor.dragging) {
		a.roadMaskPreviewPending = true
	}
	a.pumpRoadMaskPreviewRebuild(false)
}

func (a *App) saveRoadMaskFromEditor() {
	ed := a.roadMaskEditor
	if ed == nil {
		return
	}
	hadManifestPath := a.mapDef != nil && a.mapDef.RoadMaskPath != ""
	if !hadManifestPath && !ed.hasContent() {
		ed.setStatus("nothing to save")
		return
	}
	path := ed.path
	if path == "" {
		path = primaryRoadMaskPath(a.mapDef)
	}
	if path == "" {
		ed.setStatus("save failed: no map manifest")
		return
	}
	if err := ed.save(path, a.terrain); err != nil {
		ed.setStatus(err.Error())
		return
	}
	if !hadManifestPath {
		if err := ensureManifestHasRoadMask(a.mapDef, path); err != nil {
			ed.setStatus(err.Error())
			return
		}
		a.mapDef.RoadMaskPath = path
	}
	ed.setStatus(fmt.Sprintf("saved %d edges", len(ed.edges)))
}

func (a *App) rebuildRoadMaskPreview() {
	if a.terrain == nil || a.roadMaskEditor == nil {
		return
	}
	doc := a.roadMaskEditor.toRoadMaskFile(a.terrain)
	cpu, problems := prepareRoadSurfacesFromMaskFile(doc, a.terrain)
	if len(problems) > 0 {
		a.roadMaskEditor.setStatus(fmt.Sprintf("road preview failed: %v", problems[0]))
		return
	}
	replaceRoadSurfaceLayer(a.terrain, cpu)
}

func (a *App) pumpRoadMaskPreviewRebuild(force bool) {
	if !a.roadMaskPreviewPending {
		return
	}
	now := rl.GetTime()
	if !force && a.roadMaskEditor != nil && a.roadMaskEditor.dragging && now-a.roadMaskLastPreviewAt < roadMaskDragPreviewInterval {
		return
	}
	a.rebuildRoadMaskPreview()
	a.roadMaskPreviewPending = false
	a.roadMaskLastPreviewAt = now
}

func (a *App) roadMaskAimPoint(camera rl.Camera) (roadMaskVec2, bool) {
	if a.terrain == nil {
		return roadMaskVec2{}, false
	}
	screen := rl.GetMousePosition()
	if a.mouseCaptured {
		screen = rl.NewVector2(float32(rl.GetScreenWidth())*0.5, float32(rl.GetScreenHeight())*0.5)
	}
	ray := rl.GetScreenToWorldRay(screen, camera)
	hit, ok := raycastTerrainBase(a.terrain, ray)
	if !ok {
		return roadMaskVec2{}, false
	}
	return roadMaskVec2{X: hit.X, Z: hit.Z}, true
}

func raycastTerrainBase(t *terrainData, ray rl.Ray) (rl.Vector3, bool) {
	if t == nil {
		return rl.Vector3{}, false
	}
	const (
		maxDist    = float32(5000)
		stepMeters = float32(8)
	)

	prevD := float32(0)
	prevValid := false

	for d := float32(0); d <= maxDist; d += stepMeters {
		p := pointOnRay(ray, d)
		if !terrainContainsLocalXZ(t, p.X, p.Z) {
			prevValid = false
			continue
		}
		ground := terrainBaseHeightAtLocal(t, p.X, p.Z)
		if p.Y-ground <= 0 {
			if !prevValid {
				p.Y = ground
				return p, true
			}
			lo := prevD
			hi := d
			for i := 0; i < 18; i++ {
				mid := (lo + hi) * 0.5
				mp := pointOnRay(ray, mid)
				if mp.Y-terrainBaseHeightAtLocal(t, mp.X, mp.Z) > 0 {
					lo = mid
				} else {
					hi = mid
				}
			}
			p = pointOnRay(ray, hi)
			p.Y = terrainBaseHeightAtLocal(t, p.X, p.Z)
			return p, true
		}
		prevD = d
		prevValid = true
	}

	return rl.Vector3{}, false
}

func (a *App) drawRoadMaskEditor3D(camera rl.Camera) {
	ed := a.roadMaskEditor
	if ed == nil || a.terrain == nil {
		return
	}
	pos, hasPos := a.roadMaskAimPoint(camera)
	snapID, hoverNodeID, hoverEdgeID := -1, -1, -1
	var cutPt roadMaskVec2
	hasCutPt := false
	if hasPos {
		switch ed.tool {
		case roadMaskToolDraw:
			if !ed.splinePickingMid {
				snapID = ed.findSnapEndpoint(pos)
			}
		case roadMaskToolEdit:
			hoverNodeID = ed.findHitNode(pos)
			hoverEdgeID = ed.findHitEdge(pos)
		case roadMaskToolDelete:
			hoverNodeID = ed.findHitNode(pos)
			hoverEdgeID = ed.findHitEdge(pos)
		case roadMaskToolCut:
			if _, _, hp, _, found := ed.findHitEdgeAndSeg(pos); found {
				cutPt = hp
				hasCutPt = true
			}
		}
	}

	rl.DisableBackfaceCulling()
	for i := range ed.edges {
		e := &ed.edges[i]
		deleteHighlight := ed.tool == roadMaskToolDelete && e.ID == hoverEdgeID && hoverNodeID < 0
		a.drawRoadMaskEdge(ed, e, deleteHighlight)
	}
	if ed.tool == roadMaskToolEdit && ed.selEdgeID >= 0 {
		a.drawRoadMaskSelectedSegment(ed)
	}
	if ed.tool == roadMaskToolEdit {
		a.drawRoadMaskMidpoints(ed, pos, hasPos)
	}
	a.drawRoadMaskDraft(ed, pos, hasPos, snapID)
	a.drawRoadMaskNodes(ed, hoverNodeID, snapID)
	if hasCutPt {
		p := a.roadMaskWorldPoint(cutPt, roadMaskOverlayLift+0.22)
		rl.DrawSphere(p, roadMaskNodeRadius*1.25, rl.Yellow)
		rl.DrawSphereWires(p, roadMaskNodeRadius*2.0, 8, 8, rl.NewColor(255, 230, 0, 160))
	}
	if hasPos {
		p := a.roadMaskWorldPoint(pos, roadMaskOverlayLift+0.35)
		rl.DrawSphereWires(p, roadMaskNodeRadius*0.9, 8, 8, rl.White)
	}
	rl.EnableBackfaceCulling()
}

func (a *App) roadMaskWorldPoint(p roadMaskVec2, lift float32) rl.Vector3 {
	return rl.NewVector3(p.X, terrainBaseHeightAtLocal(a.terrain, p.X, p.Z)+lift, p.Z)
}

func (a *App) drawRoadMaskPolygonFill(pts []roadMaskVec2, col rl.Color) {
	for _, tri := range triangulateRoadMaskPolygon(pts) {
		rl.DrawTriangle3D(
			a.roadMaskWorldPoint(tri[0], roadMaskOverlayLift*0.55),
			a.roadMaskWorldPoint(tri[1], roadMaskOverlayLift*0.55),
			a.roadMaskWorldPoint(tri[2], roadMaskOverlayLift*0.55),
			col,
		)
	}
}

func (a *App) drawRoadMaskEdge(ed *roadMaskEditor, e *roadMaskEditEdge, deleteHighlight bool) {
	pts := ed.edgePts(e)
	for i := 0; i < len(pts)-1 && i < len(e.Segs); i++ {
		seg := e.Segs[i]
		var segPts []roadMaskVec2
		if seg.IsSpline {
			segPts = sampleRoadMaskQuadratic(pts[i], seg.MidPt, pts[i+1])
		} else {
			segPts = []roadMaskVec2{pts[i], pts[i+1]}
		}
		col := roadMaskCurbColor(seg.Curb)
		if deleteHighlight {
			col = rl.Red
		}
		a.drawRoadMaskPath(segPts, col, roadMaskLineRadius)
	}
}

func (a *App) drawRoadMaskPath(pts []roadMaskVec2, col rl.Color, _ float32) {
	for i := 1; i < len(pts); i++ {
		a3 := a.roadMaskWorldPoint(pts[i-1], roadMaskOverlayLift)
		b3 := a.roadMaskWorldPoint(pts[i], roadMaskOverlayLift)
		rl.DrawLine3D(a3, b3, col)
	}
}

func roadMaskCurbColor(c roadMaskCurbKind) rl.Color {
	if c == roadMaskCurbSoft {
		return rl.NewColor(80, 200, 255, 255)
	}
	return rl.NewColor(255, 130, 20, 255)
}

func (a *App) drawRoadMaskSelectedSegment(ed *roadMaskEditor) {
	e := ed.edgeByID(ed.selEdgeID)
	if e == nil || ed.selSegIdx >= len(e.Segs) {
		return
	}
	pts := ed.edgePts(e)
	if ed.selSegIdx+1 >= len(pts) {
		return
	}
	seg := e.Segs[ed.selSegIdx]
	segPts := []roadMaskVec2{pts[ed.selSegIdx], pts[ed.selSegIdx+1]}
	if seg.IsSpline {
		segPts = sampleRoadMaskQuadratic(pts[ed.selSegIdx], seg.MidPt, pts[ed.selSegIdx+1])
	}
	a.drawRoadMaskPath(segPts, rl.NewColor(255, 255, 255, 130), roadMaskLineRadius*1.8)
}

func (a *App) drawRoadMaskMidpoints(ed *roadMaskEditor, aim roadMaskVec2, hasAim bool) {
	hoverEdgeID, hoverSegIdx := -1, -1
	if hasAim {
		hoverEdgeID, hoverSegIdx = ed.findHitMidPt(aim)
	}
	for i := range ed.edges {
		e := &ed.edges[i]
		pts := ed.edgePts(e)
		for si, seg := range e.Segs {
			if !seg.IsSpline || si+1 >= len(pts) {
				continue
			}
			start := a.roadMaskWorldPoint(pts[si], roadMaskOverlayLift+0.07)
			ctrl := a.roadMaskWorldPoint(seg.MidPt, roadMaskOverlayLift+0.12)
			end := a.roadMaskWorldPoint(pts[si+1], roadMaskOverlayLift+0.07)
			guidCol := rl.NewColor(220, 220, 80, 100)
			rl.DrawLine3D(start, ctrl, guidCol)
			rl.DrawLine3D(end, ctrl, guidCol)
			col := rl.NewColor(180, 180, 55, 190)
			if e.ID == ed.selEdgeID && si == ed.selSegIdx {
				col = rl.Yellow
			} else if e.ID == hoverEdgeID && si == hoverSegIdx {
				col = rl.NewColor(230, 230, 100, 230)
			}
			rl.DrawSphere(ctrl, roadMaskNodeRadius*0.9, col)
			rl.DrawSphereWires(ctrl, roadMaskNodeRadius*1.7, 8, 8, rl.NewColor(col.R, col.G, col.B, 120))
		}
	}
}

func (a *App) drawRoadMaskDraft(ed *roadMaskEditor, aim roadMaskVec2, hasAim bool, snapID int) {
	if !ed.drawing || len(ed.draftNodeIDs) == 0 {
		return
	}
	cursor := aim
	if hasAim && !ed.splinePickingMid && snapID >= 0 {
		if n := ed.nodeByID(snapID); n != nil {
			cursor = n.Pos
		}
	}

	pts := make([]roadMaskVec2, 0, len(ed.draftNodeIDs))
	for _, id := range ed.draftNodeIDs {
		if n := ed.nodeByID(id); n != nil {
			pts = append(pts, n.Pos)
		}
	}
	for i, seg := range ed.draftSegProps {
		if i+1 >= len(pts) {
			break
		}
		segPts := []roadMaskVec2{pts[i], pts[i+1]}
		if seg.IsSpline {
			segPts = sampleRoadMaskQuadratic(pts[i], seg.MidPt, pts[i+1])
		}
		col := roadMaskCurbColor(seg.Curb)
		col.A = 210
		a.drawRoadMaskPath(segPts, col, roadMaskLineRadius)
	}
	if hasAim && ed.splinePickingMid && len(pts) >= 2 {
		start := pts[len(pts)-2]
		end := pts[len(pts)-1]
		col := roadMaskCurbColor(ed.splinePendingCurb)
		col.A = 210
		a.drawRoadMaskPath(sampleRoadMaskQuadratic(start, cursor, end), col, roadMaskLineRadius)
		rl.DrawLine3D(a.roadMaskWorldPoint(start, roadMaskOverlayLift+0.08), a.roadMaskWorldPoint(cursor, roadMaskOverlayLift+0.12), rl.NewColor(220, 220, 80, 120))
		rl.DrawLine3D(a.roadMaskWorldPoint(end, roadMaskOverlayLift+0.08), a.roadMaskWorldPoint(cursor, roadMaskOverlayLift+0.12), rl.NewColor(220, 220, 80, 120))
		rl.DrawSphere(a.roadMaskWorldPoint(cursor, roadMaskOverlayLift+0.15), roadMaskNodeRadius, rl.Yellow)
	} else if hasAim && len(pts) >= 1 {
		col := roadMaskCurbColor(ed.currentCurb)
		col.A = 150
		a.drawRoadMaskPath([]roadMaskVec2{pts[len(pts)-1], cursor}, col, roadMaskLineRadius*0.8)
	}
	for _, id := range ed.draftNodeIDs {
		if n := ed.nodeByID(id); n != nil {
			rl.DrawSphere(a.roadMaskWorldPoint(n.Pos, roadMaskOverlayLift+0.1), roadMaskNodeRadius, rl.NewColor(140, 255, 100, 230))
		}
	}
}

func (a *App) drawRoadMaskNodes(ed *roadMaskEditor, hoverNodeID, snapNodeID int) {
	for _, n := range ed.nodes {
		col := rl.NewColor(210, 210, 210, 255)
		if n.ID == ed.selNodeID {
			col = rl.Yellow
		} else if ed.tool == roadMaskToolDelete && n.ID == hoverNodeID {
			col = rl.Red
		} else if ed.nodeEdgeCount(n.ID) > 1 {
			col = rl.NewColor(255, 200, 60, 255)
		}
		p := a.roadMaskWorldPoint(n.Pos, roadMaskOverlayLift+0.1)
		rl.DrawSphere(p, roadMaskNodeRadius, col)
	}
	if snapNodeID >= 0 {
		if n := ed.nodeByID(snapNodeID); n != nil {
			col := rl.NewColor(0, 255, 200, 230)
			if ed.isClosingSnap(snapNodeID) {
				col = rl.NewColor(255, 80, 220, 230)
			}
			p := a.roadMaskWorldPoint(n.Pos, roadMaskOverlayLift+0.15)
			rl.DrawSphereWires(p, roadMaskSnapMeters*0.45, 16, 8, col)
			rl.DrawSphere(p, roadMaskNodeRadius*0.7, col)
		}
	}
}

func (a *App) drawRoadMaskEditorHUD(w, h int32) {
	ed := a.roadMaskEditor
	if ed == nil {
		return
	}
	if a.mouseCaptured {
		cx, cy := w/2, h/2
		rl.DrawCircleLines(cx, cy, 6, rl.NewColor(255, 255, 255, 220))
		rl.DrawLine(cx-16, cy, cx-8, cy, rl.NewColor(255, 255, 255, 220))
		rl.DrawLine(cx+8, cy, cx+16, cy, rl.NewColor(255, 255, 255, 220))
		rl.DrawLine(cx, cy-16, cx, cy-8, rl.NewColor(255, 255, 255, 220))
		rl.DrawLine(cx, cy+8, cx, cy+16, rl.NewColor(255, 255, 255, 220))
	}

	dirty := ""
	if ed.dirty {
		dirty = " *"
	}
	kind := "line"
	if ed.currentIsSpline {
		kind = "spline"
	}
	lines := []string{
		fmt.Sprintf("Road mask: %s%s", ed.toolName(), dirty),
		fmt.Sprintf("Pointer: %s", a.roadMaskPointerMode()),
		fmt.Sprintf("Segment: %s | %s", kind, ed.currentCurb.String()),
		fmt.Sprintf("Edges: %d (%d closed)  Nodes: %d", len(ed.edges), ed.closedEdgeCount(), len(ed.nodes)),
	}
	if ed.path != "" {
		lines = append(lines, filepath.ToSlash(ed.path))
	}
	if ed.status != "" && rl.GetTime() < ed.statusUntil {
		lines = append(lines, ed.status)
	}

	x := int32(8)
	y := int32(34)
	width := int32(620)
	lineH := int32(18)
	height := int32(len(lines))*lineH + 12
	rl.DrawRectangle(x-4, y-6, width, height, rl.NewColor(20, 22, 26, 190))
	rl.DrawRectangleLines(x-4, y-6, width, height, rl.NewColor(90, 100, 110, 220))
	for i, line := range lines {
		col := rl.White
		if i == len(lines)-1 && ed.status != "" && line == ed.status {
			col = rl.NewColor(255, 210, 80, 255)
		}
		rl.DrawText(line, x, y+int32(i)*lineH, 14, col)
	}
}

func (a *App) roadMaskPointerMode() string {
	if a.mouseCaptured {
		return "captured center aim (Tab toggles)"
	}
	return "mouse cursor (Tab toggles)"
}

func newRoadMaskEditor(mapDef *mapDefinition, terrain *terrainData) *roadMaskEditor {
	ed := &roadMaskEditor{
		nextNodeID:  1,
		nextEdgeID:  1,
		tool:        roadMaskToolDraw,
		currentCurb: roadMaskCurbHard,
		selNodeID:   -1,
		selEdgeID:   -1,
		selSegIdx:   -1,
		path:        primaryRoadMaskPath(mapDef),
		geo:         syntheticRoadMaskGeo(terrain),
	}
	return ed
}

func loadRoadMaskEditor(mapDef *mapDefinition, terrain *terrainData) (*roadMaskEditor, error) {
	ed := newRoadMaskEditor(mapDef, terrain)
	if ed.path == "" {
		return ed, nil
	}
	if err := ed.load(ed.path, terrain); err != nil {
		if os.IsNotExist(err) {
			ed.setStatus("new road mask")
			return ed, nil
		}
		return nil, err
	}
	ed.setStatus(fmt.Sprintf("loaded %d edges", len(ed.edges)))
	return ed, nil
}

func (ed *roadMaskEditor) setStatus(msg string) {
	ed.status = msg
	if msg == "" {
		ed.statusUntil = 0
		return
	}
	ed.statusUntil = rl.GetTime() + 4
}

func (ed *roadMaskEditor) nodeByID(id int) *roadMaskEditNode {
	for i := range ed.nodes {
		if ed.nodes[i].ID == id {
			return &ed.nodes[i]
		}
	}
	return nil
}

func (ed *roadMaskEditor) edgeByID(id int) *roadMaskEditEdge {
	for i := range ed.edges {
		if ed.edges[i].ID == id {
			return &ed.edges[i]
		}
	}
	return nil
}

func (ed *roadMaskEditor) addNode(pos roadMaskVec2) int {
	id := ed.nextNodeID
	ed.nextNodeID++
	ed.nodes = append(ed.nodes, roadMaskEditNode{ID: id, Pos: pos})
	return id
}

func (ed *roadMaskEditor) addEdge(nodeIDs []int, segs []roadMaskEditSegProps) int {
	id := ed.nextEdgeID
	ed.nextEdgeID++
	ids := append([]int(nil), nodeIDs...)
	sc := append([]roadMaskEditSegProps(nil), segs...)
	ed.edges = append(ed.edges, roadMaskEditEdge{ID: id, NodeIDs: ids, Segs: sc})
	return id
}

func (ed *roadMaskEditor) findSnapEndpoint(pos roadMaskVec2) int {
	best := float32(math.MaxFloat32)
	bestID := -1

	lastDraft := -1
	if len(ed.draftNodeIDs) > 0 {
		lastDraft = ed.draftNodeIDs[len(ed.draftNodeIDs)-1]
	}

	seen := map[int]bool{}
	for i := range ed.edges {
		nids := ed.edges[i].NodeIDs
		if len(nids) == 0 {
			continue
		}
		for _, candidate := range []int{nids[0], nids[len(nids)-1]} {
			if seen[candidate] || candidate == lastDraft {
				continue
			}
			seen[candidate] = true
			n := ed.nodeByID(candidate)
			if n == nil {
				continue
			}
			d2 := roadMaskDist2(n.Pos, pos)
			if d2 < roadMaskSnapMeters*roadMaskSnapMeters && d2 < best {
				best = d2
				bestID = candidate
			}
		}
	}

	for i, id := range ed.draftNodeIDs {
		if i == len(ed.draftNodeIDs)-1 {
			break
		}
		if seen[id] {
			continue
		}
		n := ed.nodeByID(id)
		if n == nil {
			continue
		}
		d2 := roadMaskDist2(n.Pos, pos)
		if d2 < roadMaskSnapMeters*roadMaskSnapMeters && d2 < best {
			best = d2
			bestID = id
		}
	}
	return bestID
}

func (ed *roadMaskEditor) isClosingSnap(snapID int) bool {
	return ed.drawing && snapID >= 0 && len(ed.draftNodeIDs) >= 2 && snapID == ed.draftNodeIDs[0]
}

func (ed *roadMaskEditor) findHitNode(pos roadMaskVec2) int {
	best := float32(math.MaxFloat32)
	bestID := -1
	for _, n := range ed.nodes {
		d2 := roadMaskDist2(n.Pos, pos)
		if d2 < roadMaskHoverMeters*roadMaskHoverMeters && d2 < best {
			best = d2
			bestID = n.ID
		}
	}
	return bestID
}

func (ed *roadMaskEditor) findHitMidPt(pos roadMaskVec2) (edgeID, segIdx int) {
	best := float32(math.MaxFloat32)
	edgeID, segIdx = -1, -1
	for i := range ed.edges {
		e := &ed.edges[i]
		for si, seg := range e.Segs {
			if !seg.IsSpline {
				continue
			}
			d2 := roadMaskDist2(seg.MidPt, pos)
			if d2 < roadMaskHoverMeters*roadMaskHoverMeters && d2 < best {
				best = d2
				edgeID = e.ID
				segIdx = si
			}
		}
	}
	return edgeID, segIdx
}

func (ed *roadMaskEditor) findHitEdge(pos roadMaskVec2) int {
	best := roadMaskHoverMeters
	bestID := -1
	for i := range ed.edges {
		if d := ed.distToEdge(&ed.edges[i], pos); d < best {
			best = d
			bestID = ed.edges[i].ID
		}
	}
	return bestID
}

func (ed *roadMaskEditor) distToEdge(e *roadMaskEditEdge, pos roadMaskVec2) float32 {
	best := float32(math.MaxFloat32)
	pts := ed.edgePts(e)
	for i := 0; i < len(pts)-1 && i < len(e.Segs); i++ {
		var segPts []roadMaskVec2
		if e.Segs[i].IsSpline {
			segPts = sampleRoadMaskQuadratic(pts[i], e.Segs[i].MidPt, pts[i+1])
		} else {
			segPts = []roadMaskVec2{pts[i], pts[i+1]}
		}
		for j := 1; j < len(segPts); j++ {
			_, _, d := closestRoadMaskPointOnSegment(pos, segPts[j-1], segPts[j])
			if d < best {
				best = d
			}
		}
	}
	return best
}

func closestRoadMaskPointOnSegment(p, a, b roadMaskVec2) (roadMaskVec2, float32, float32) {
	ab := b.Sub(a)
	denom := ab.LenSq()
	t := float32(0)
	if denom > 1e-6 {
		t = p.Sub(a).Dot(ab) / denom
		if t < 0 {
			t = 0
		} else if t > 1 {
			t = 1
		}
	}
	cp := a.Lerp(b, t)
	return cp, t, roadMaskDist(cp, p)
}

func (ed *roadMaskEditor) findHitEdgeAndSeg(pos roadMaskVec2) (edgeID, segIdx int, hitPt roadMaskVec2, hitT float32, found bool) {
	best := roadMaskHoverMeters
	edgeID = -1
	for i := range ed.edges {
		e := &ed.edges[i]
		pts := ed.edgePts(e)
		for si := 0; si < len(pts)-1 && si < len(e.Segs); si++ {
			if e.Segs[si].IsSpline {
				segPts := sampleRoadMaskQuadratic(pts[si], e.Segs[si].MidPt, pts[si+1])
				for j := 1; j < len(segPts); j++ {
					cp, localT, d := closestRoadMaskPointOnSegment(pos, segPts[j-1], segPts[j])
					if d < best {
						best = d
						edgeID = e.ID
						segIdx = si
						hitPt = cp
						hitT = (float32(j-1) + localT) / float32(len(segPts)-1)
						found = true
					}
				}
				continue
			}
			cp, t, d := closestRoadMaskPointOnSegment(pos, pts[si], pts[si+1])
			if d < best {
				best = d
				edgeID = e.ID
				segIdx = si
				hitPt = cp
				hitT = t
				found = true
			}
		}
	}
	return edgeID, segIdx, hitPt, hitT, found
}

func (ed *roadMaskEditor) removeEdgeOnly(edgeID int) {
	out := ed.edges[:0]
	for _, e := range ed.edges {
		if e.ID != edgeID {
			out = append(out, e)
		}
	}
	ed.edges = out
}

func (ed *roadMaskEditor) edgePts(e *roadMaskEditEdge) []roadMaskVec2 {
	out := make([]roadMaskVec2, 0, len(e.NodeIDs))
	for _, id := range e.NodeIDs {
		if n := ed.nodeByID(id); n != nil {
			out = append(out, n.Pos)
		}
	}
	return out
}

func (ed *roadMaskEditor) nodeEdgeCount(nodeID int) int {
	count := 0
	for i := range ed.edges {
		for _, id := range ed.edges[i].NodeIDs {
			if id == nodeID {
				count++
				break
			}
		}
	}
	return count
}

func (ed *roadMaskEditor) isNodeUsed(nodeID int) bool {
	return ed.nodeEdgeCount(nodeID) > 0
}

func (ed *roadMaskEditor) removeNodeRaw(nodeID int) {
	out := ed.nodes[:0]
	for _, n := range ed.nodes {
		if n.ID != nodeID {
			out = append(out, n)
		}
	}
	ed.nodes = out
}

func (ed *roadMaskEditor) deleteEdge(edgeID int) {
	var removedIDs []int
	out := ed.edges[:0]
	for _, e := range ed.edges {
		if e.ID == edgeID {
			removedIDs = e.NodeIDs
		} else {
			out = append(out, e)
		}
	}
	ed.edges = out
	for _, nid := range removedIDs {
		if !ed.isNodeUsed(nid) {
			ed.removeNodeRaw(nid)
		}
	}
}

func (ed *roadMaskEditor) deleteNode(nodeID int) {
	var newEdges []roadMaskEditEdge
	for _, e := range ed.edges {
		segs := splitRoadMaskAtNode(e, nodeID)
		for _, seg := range segs {
			seg.ID = ed.nextEdgeID
			ed.nextEdgeID++
			newEdges = append(newEdges, seg)
		}
	}
	ed.edges = newEdges
	ed.removeNodeRaw(nodeID)
}

func splitRoadMaskAtNode(e roadMaskEditEdge, nodeID int) []roadMaskEditEdge {
	idx := -1
	for i, id := range e.NodeIDs {
		if id == nodeID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return []roadMaskEditEdge{e}
	}
	var result []roadMaskEditEdge
	if idx >= 2 {
		result = append(result, roadMaskEditEdge{
			NodeIDs: append([]int(nil), e.NodeIDs[:idx]...),
			Segs:    append([]roadMaskEditSegProps(nil), e.Segs[:idx-1]...),
		})
	}
	if len(e.NodeIDs)-idx-1 >= 2 {
		result = append(result, roadMaskEditEdge{
			NodeIDs: append([]int(nil), e.NodeIDs[idx+1:]...),
			Segs:    append([]roadMaskEditSegProps(nil), e.Segs[idx+1:]...),
		})
	}
	return result
}

func roadMaskEditEdgeClosed(e *roadMaskEditEdge) bool {
	return e != nil && len(e.NodeIDs) >= 4 && e.NodeIDs[0] == e.NodeIDs[len(e.NodeIDs)-1]
}

func (ed *roadMaskEditor) sampleAllSegments(e *roadMaskEditEdge) []roadMaskVec2 {
	pts := ed.edgePts(e)
	if len(pts) < 2 {
		return pts
	}
	out := make([]roadMaskVec2, 0, len(pts)*roadMaskCurveSteps)
	for i := 0; i < len(pts)-1 && i < len(e.Segs); i++ {
		if e.Segs[i].IsSpline {
			seg := sampleRoadMaskQuadratic(pts[i], e.Segs[i].MidPt, pts[i+1])
			out = append(out, seg[:len(seg)-1]...)
		} else {
			out = append(out, pts[i])
		}
	}
	out = append(out, pts[len(pts)-1])
	return out
}

func (ed *roadMaskEditor) handleInput(pos roadMaskVec2, hasPos bool) bool {
	changed := false
	ctrl := rl.IsKeyDown(rl.KeyLeftControl) || rl.IsKeyDown(rl.KeyRightControl)

	if rl.IsKeyPressed(rl.KeyEscape) {
		changed = ed.cancelSelectionOrDraft() || changed
	}

	if !ctrl {
		if rl.IsKeyPressed(rl.KeyG) {
			changed = ed.switchTool(roadMaskToolDraw) || changed
		}
		if rl.IsKeyPressed(rl.KeyV) {
			changed = ed.switchTool(roadMaskToolEdit) || changed
		}
		if rl.IsKeyPressed(rl.KeyR) {
			changed = ed.switchTool(roadMaskToolDelete) || changed
		}
		if rl.IsKeyPressed(rl.KeyX) {
			changed = ed.switchTool(roadMaskToolCut) || changed
		}
		if rl.IsKeyPressed(rl.KeyZ) || rl.IsKeyPressed(rl.KeyL) {
			ed.currentIsSpline = false
		}
		if rl.IsKeyPressed(rl.KeyC) {
			ed.currentIsSpline = true
		}
		if rl.IsKeyPressed(rl.KeyH) {
			ed.currentCurb = roadMaskCurbHard
		}
		if rl.IsKeyPressed(rl.KeyF) {
			ed.currentCurb = roadMaskCurbSoft
		}
	}

	if !hasPos {
		return changed
	}

	switch ed.tool {
	case roadMaskToolDraw:
		changed = ed.handleDraw(pos) || changed
	case roadMaskToolEdit:
		changed = ed.handleEdit(pos) || changed
	case roadMaskToolDelete:
		changed = ed.handleDelete(pos) || changed
	case roadMaskToolCut:
		changed = ed.handleCut(pos) || changed
	}
	if changed {
		ed.dirty = true
	}
	return changed
}

func (ed *roadMaskEditor) cancelSelectionOrDraft() bool {
	changed := false
	if ed.drawing {
		if ed.splinePickingMid {
			pendingEndID := ed.draftNodeIDs[len(ed.draftNodeIDs)-1]
			ed.draftNodeIDs = ed.draftNodeIDs[:len(ed.draftNodeIDs)-1]
			changed = ed.removeDraftNodeIfUnused(pendingEndID) || changed
			ed.splinePickingMid = false
		} else {
			for _, id := range ed.draftNodeIDs {
				if !ed.isNodeUsed(id) {
					ed.removeNodeRaw(id)
					changed = true
				}
			}
			ed.drawing = false
			ed.draftNodeIDs = nil
			ed.draftSegProps = nil
		}
	}
	ed.selNodeID = -1
	ed.selEdgeID = -1
	ed.selSegIdx = -1
	ed.dragging = false
	return changed
}

func (ed *roadMaskEditor) switchTool(t roadMaskEditTool) bool {
	if t == ed.tool {
		return false
	}
	changed := false
	if ed.drawing {
		changed = ed.finishDraw()
	}
	ed.drawing = false
	ed.draftNodeIDs = nil
	ed.draftSegProps = nil
	ed.splinePickingMid = false
	ed.selNodeID = -1
	ed.selEdgeID = -1
	ed.selSegIdx = -1
	ed.dragging = false
	ed.tool = t
	return changed
}

func (ed *roadMaskEditor) handleDraw(pos roadMaskVec2) bool {
	if ed.splinePickingMid {
		if rl.IsMouseButtonPressed(rl.MouseLeftButton) {
			ed.draftSegProps = append(ed.draftSegProps, roadMaskEditSegProps{
				IsSpline: true,
				Curb:     ed.splinePendingCurb,
				MidPt:    pos,
			})
			ed.splinePickingMid = false
			n := len(ed.draftNodeIDs)
			if n >= 3 && ed.draftNodeIDs[0] == ed.draftNodeIDs[n-1] {
				return ed.finishDraw()
			}
			return true
		}
		if rl.IsMouseButtonPressed(rl.MouseRightButton) {
			return ed.undoLastDraftSegment()
		}
		if rl.IsKeyPressed(rl.KeyEnter) {
			return ed.finishDraw()
		}
		return false
	}

	snapID := ed.findSnapEndpoint(pos)
	changed := false
	if rl.IsMouseButtonPressed(rl.MouseLeftButton) {
		nodeID := snapID
		if nodeID < 0 {
			nodeID = ed.addNode(pos)
			changed = true
		}

		if !ed.drawing {
			ed.drawing = true
			ed.draftNodeIDs = []int{nodeID}
			ed.draftSegProps = nil
		} else {
			last := ed.draftNodeIDs[len(ed.draftNodeIDs)-1]
			if nodeID != last {
				ed.draftNodeIDs = append(ed.draftNodeIDs, nodeID)
				changed = true
				if ed.currentIsSpline {
					ed.splinePickingMid = true
					ed.splinePendingCurb = ed.currentCurb
				} else {
					ed.draftSegProps = append(ed.draftSegProps, roadMaskEditSegProps{
						IsSpline: false,
						Curb:     ed.currentCurb,
					})
					if ed.isClosingSnap(snapID) && len(ed.draftNodeIDs) >= 3 {
						return ed.finishDraw() || changed
					}
				}
			}
		}
	}

	if ed.drawing {
		if rl.IsMouseButtonPressed(rl.MouseRightButton) {
			changed = ed.undoLastDraftSegment() || changed
		}
		if len(ed.draftNodeIDs) >= 2 && rl.IsKeyPressed(rl.KeyEnter) {
			changed = ed.finishDraw() || changed
		}
	}
	return changed
}

func (ed *roadMaskEditor) undoLastDraftSegment() bool {
	if !ed.drawing {
		return false
	}
	changed := false
	if ed.splinePickingMid {
		lastNodeID := ed.draftNodeIDs[len(ed.draftNodeIDs)-1]
		ed.draftNodeIDs = ed.draftNodeIDs[:len(ed.draftNodeIDs)-1]
		changed = ed.removeDraftNodeIfUnused(lastNodeID) || changed
		ed.splinePickingMid = false
		ed.setStatus("pending segment removed")
		return changed
	}
	if len(ed.draftSegProps) > 0 && len(ed.draftNodeIDs) > 1 {
		lastNodeID := ed.draftNodeIDs[len(ed.draftNodeIDs)-1]
		ed.draftNodeIDs = ed.draftNodeIDs[:len(ed.draftNodeIDs)-1]
		ed.draftSegProps = ed.draftSegProps[:len(ed.draftSegProps)-1]
		changed = ed.removeDraftNodeIfUnused(lastNodeID) || changed
		ed.setStatus("last segment removed")
		return true
	}
	if len(ed.draftNodeIDs) == 1 {
		lastNodeID := ed.draftNodeIDs[0]
		ed.draftNodeIDs = nil
		ed.draftSegProps = nil
		ed.drawing = false
		changed = ed.removeDraftNodeIfUnused(lastNodeID) || changed
		ed.setStatus("draft cancelled")
	}
	return changed
}

func (ed *roadMaskEditor) removeDraftNodeIfUnused(nodeID int) bool {
	if ed.isNodeUsed(nodeID) {
		return false
	}
	for _, id := range ed.draftNodeIDs {
		if id == nodeID {
			return false
		}
	}
	ed.removeNodeRaw(nodeID)
	return true
}

func (ed *roadMaskEditor) finishDraw() bool {
	changed := false
	if ed.splinePickingMid {
		pendingEndID := ed.draftNodeIDs[len(ed.draftNodeIDs)-1]
		ed.draftNodeIDs = ed.draftNodeIDs[:len(ed.draftNodeIDs)-1]
		changed = ed.removeDraftNodeIfUnused(pendingEndID) || changed
		ed.splinePickingMid = false
	}
	if len(ed.draftNodeIDs) >= 2 {
		ed.addEdge(ed.draftNodeIDs, ed.draftSegProps)
		ed.setStatus(fmt.Sprintf("edge added (%d nodes)", len(ed.draftNodeIDs)))
		changed = true
	} else if len(ed.draftNodeIDs) == 1 && !ed.isNodeUsed(ed.draftNodeIDs[0]) {
		ed.removeNodeRaw(ed.draftNodeIDs[0])
		changed = true
	}
	ed.drawing = false
	ed.draftNodeIDs = nil
	ed.draftSegProps = nil
	return changed
}

func (ed *roadMaskEditor) handleEdit(pos roadMaskVec2) bool {
	changed := false
	if rl.IsMouseButtonPressed(rl.MouseLeftButton) && !ed.dragging {
		midEdgeID, midSegIdx := ed.findHitMidPt(pos)
		if midEdgeID >= 0 {
			ed.selEdgeID = midEdgeID
			ed.selSegIdx = midSegIdx
			ed.selNodeID = -1
			ed.dragging = true
		} else if hitID := ed.findHitNode(pos); hitID >= 0 {
			ed.selNodeID = hitID
			ed.selEdgeID = -1
			ed.selSegIdx = -1
			ed.dragging = true
		} else if eid, si, _, _, found := ed.findHitEdgeAndSeg(pos); found {
			ed.selEdgeID = eid
			ed.selSegIdx = si
			ed.selNodeID = -1
			ed.setSegStatus()
		} else {
			ed.selNodeID = -1
			ed.selEdgeID = -1
			ed.selSegIdx = -1
		}
	}
	if rl.IsMouseButtonDown(rl.MouseLeftButton) && ed.dragging {
		if ed.selEdgeID >= 0 {
			if e := ed.edgeByID(ed.selEdgeID); e != nil && ed.selSegIdx < len(e.Segs) {
				e.Segs[ed.selSegIdx].MidPt = pos
				changed = true
			}
		} else if ed.selNodeID >= 0 {
			if n := ed.nodeByID(ed.selNodeID); n != nil {
				n.Pos = pos
				changed = true
			}
		}
	}
	if rl.IsMouseButtonReleased(rl.MouseLeftButton) {
		ed.dragging = false
	}

	if ed.selEdgeID >= 0 {
		if e := ed.edgeByID(ed.selEdgeID); e != nil && ed.selSegIdx < len(e.Segs) {
			seg := &e.Segs[ed.selSegIdx]
			switch {
			case rl.IsKeyPressed(rl.KeyZ) || rl.IsKeyPressed(rl.KeyL):
				if seg.IsSpline {
					seg.IsSpline = false
					changed = true
				}
			case rl.IsKeyPressed(rl.KeyC):
				if !seg.IsSpline {
					pts := ed.edgePts(e)
					if ed.selSegIdx+1 < len(pts) {
						seg.MidPt = pts[ed.selSegIdx].Lerp(pts[ed.selSegIdx+1], 0.5)
					}
					seg.IsSpline = true
					changed = true
				}
			case rl.IsKeyPressed(rl.KeyH):
				if seg.Curb != roadMaskCurbHard {
					seg.Curb = roadMaskCurbHard
					changed = true
				}
			case rl.IsKeyPressed(rl.KeyF):
				if seg.Curb != roadMaskCurbSoft {
					seg.Curb = roadMaskCurbSoft
					changed = true
				}
			}
			if changed {
				ed.setSegStatus()
			}
		}
	}

	if rl.IsKeyPressed(rl.KeyDelete) || rl.IsKeyPressed(rl.KeyBackspace) {
		if ed.selEdgeID >= 0 {
			if e := ed.edgeByID(ed.selEdgeID); e != nil && ed.selSegIdx < len(e.Segs) {
				if e.Segs[ed.selSegIdx].IsSpline {
					e.Segs[ed.selSegIdx].IsSpline = false
					changed = true
				}
			}
			ed.selEdgeID = -1
			ed.selSegIdx = -1
			ed.setStatus("segment straightened")
		} else if ed.selNodeID >= 0 {
			ed.deleteNode(ed.selNodeID)
			ed.selNodeID = -1
			ed.setStatus("node deleted")
			changed = true
		}
	}
	return changed
}

func (ed *roadMaskEditor) setSegStatus() {
	e := ed.edgeByID(ed.selEdgeID)
	if e == nil || ed.selSegIdx >= len(e.Segs) {
		return
	}
	seg := e.Segs[ed.selSegIdx]
	typeStr := "line"
	if seg.IsSpline {
		typeStr = "spline"
	}
	ed.setStatus(fmt.Sprintf("segment: %s | %s", typeStr, seg.Curb.String()))
}

func (ed *roadMaskEditor) handleDelete(pos roadMaskVec2) bool {
	if !rl.IsMouseButtonPressed(rl.MouseLeftButton) {
		return false
	}
	nodeID := ed.findHitNode(pos)
	if nodeID >= 0 {
		ed.deleteNode(nodeID)
		ed.setStatus("node deleted")
		return true
	}
	edgeID := ed.findHitEdge(pos)
	if edgeID >= 0 {
		ed.deleteEdge(edgeID)
		ed.setStatus("edge deleted")
		return true
	}
	return false
}

func (ed *roadMaskEditor) handleCut(pos roadMaskVec2) bool {
	if !rl.IsMouseButtonPressed(rl.MouseLeftButton) {
		return false
	}
	eid, si, hitPt, hitT, found := ed.findHitEdgeAndSeg(pos)
	if !found {
		return false
	}
	e := ed.edgeByID(eid)
	if e == nil || si < 0 || si >= len(e.Segs) {
		return false
	}

	seg := e.Segs[si]
	leftSeg, rightSeg := splitRoadMaskSegmentProps(ed, e, si, hitT)
	if seg.IsSpline {
		a := ed.nodeByID(e.NodeIDs[si])
		b := ed.nodeByID(e.NodeIDs[si+1])
		if a == nil || b == nil {
			return false
		}
		hitPt, _, _ = splitRoadMaskQuadratic(a.Pos, seg.MidPt, b.Pos, hitT)
	}
	newID := ed.addNode(hitPt)

	if roadMaskEditEdgeClosed(e) {
		newNodeIDs := make([]int, len(e.NodeIDs)+1)
		copy(newNodeIDs[:si+1], e.NodeIDs[:si+1])
		newNodeIDs[si+1] = newID
		copy(newNodeIDs[si+2:], e.NodeIDs[si+1:])

		newSegs := make([]roadMaskEditSegProps, len(e.Segs)+1)
		copy(newSegs[:si], e.Segs[:si])
		newSegs[si] = leftSeg
		newSegs[si+1] = rightSeg
		copy(newSegs[si+2:], e.Segs[si+1:])

		ed.removeEdgeOnly(eid)
		ed.addEdge(newNodeIDs, newSegs)
		ed.setStatus("node inserted into polygon")
		return true
	}

	leftIDs := make([]int, si+2)
	copy(leftIDs, e.NodeIDs[:si+1])
	leftIDs[si+1] = newID
	leftSegs := make([]roadMaskEditSegProps, si+1)
	copy(leftSegs, e.Segs[:si])
	leftSegs[si] = leftSeg

	rightIDs := make([]int, len(e.NodeIDs)-si)
	rightIDs[0] = newID
	copy(rightIDs[1:], e.NodeIDs[si+1:])
	rightSegs := make([]roadMaskEditSegProps, len(e.Segs)-si)
	rightSegs[0] = rightSeg
	copy(rightSegs[1:], e.Segs[si+1:])

	ed.removeEdgeOnly(eid)
	ed.addEdge(leftIDs, leftSegs)
	ed.addEdge(rightIDs, rightSegs)
	ed.setStatus("edge cut")
	return true
}

func splitRoadMaskSegmentProps(ed *roadMaskEditor, e *roadMaskEditEdge, segIdx int, t float32) (roadMaskEditSegProps, roadMaskEditSegProps) {
	seg := e.Segs[segIdx]
	leftSeg, rightSeg := seg, seg
	if !seg.IsSpline {
		return leftSeg, rightSeg
	}
	a := ed.nodeByID(e.NodeIDs[segIdx])
	b := ed.nodeByID(e.NodeIDs[segIdx+1])
	if a == nil || b == nil {
		return leftSeg, rightSeg
	}
	_, leftCtrl, rightCtrl := splitRoadMaskQuadratic(a.Pos, seg.MidPt, b.Pos, t)
	leftSeg.MidPt = leftCtrl
	rightSeg.MidPt = rightCtrl
	return leftSeg, rightSeg
}

func sampleRoadMaskQuadratic(p0, ctrl, p2 roadMaskVec2) []roadMaskVec2 {
	out := make([]roadMaskVec2, 0, roadMaskCurveSteps+1)
	for s := 0; s <= roadMaskCurveSteps; s++ {
		t := float32(s) / float32(roadMaskCurveSteps)
		out = append(out, roadMaskQuadraticPoint(p0, ctrl, p2, t))
	}
	return out
}

func roadMaskQuadraticPoint(p0, ctrl, p2 roadMaskVec2, t float32) roadMaskVec2 {
	u := 1 - t
	return p0.Scale(u * u).Add(ctrl.Scale(2 * u * t)).Add(p2.Scale(t * t))
}

func splitRoadMaskQuadratic(p0, ctrl, p2 roadMaskVec2, t float32) (cutPt, leftCtrl, rightCtrl roadMaskVec2) {
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	leftCtrl = p0.Lerp(ctrl, t)
	rightCtrl = ctrl.Lerp(p2, t)
	cutPt = leftCtrl.Lerp(rightCtrl, t)
	return cutPt, leftCtrl, rightCtrl
}

func parseRoadMaskCurbKind(s string) roadMaskCurbKind {
	if s == "soft" {
		return roadMaskCurbSoft
	}
	return roadMaskCurbHard
}

func (ed *roadMaskEditor) load(path string, terrain *terrainData) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc roadMaskFile
	if err := json.Unmarshal(b, &doc); err != nil {
		return err
	}
	if doc.Geotiff.GeoTransform[1] != 0 || doc.Geotiff.GeoTransform[5] != 0 {
		ed.geo = doc.Geotiff
	}

	ed.nodes = nil
	ed.edges = nil
	maxNode, maxEdge := 0, 0
	for _, sn := range doc.Nodes {
		local := roadMaskPixelToLocal(doc.Geotiff.GeoTransform, terrain, sn.X, sn.Y)
		ed.nodes = append(ed.nodes, roadMaskEditNode{ID: sn.ID, Pos: roadMaskVec2{X: local.X, Z: local.Z}})
		if sn.ID > maxNode {
			maxNode = sn.ID
		}
	}
	for _, se := range doc.Edges {
		ids := append([]int(nil), se.NodeIDs...)
		srcSegs := roadMaskEdgeSegments(se)
		n := len(ids) - 1
		if n < 0 {
			n = 0
		}
		if len(srcSegs) < n {
			padded := make([]roadMaskSeg, n)
			copy(padded, srcSegs)
			for i := len(srcSegs); i < n; i++ {
				padded[i] = roadMaskSeg{Curb: string(roadCurbHard)}
			}
			srcSegs = padded
		}
		if len(srcSegs) > n {
			srcSegs = srcSegs[:n]
		}
		segs := make([]roadMaskEditSegProps, n)
		for i, s := range srcSegs {
			sp := roadMaskEditSegProps{Curb: parseRoadMaskCurbKind(s.Curb)}
			if s.IsSpline {
				ctrl := roadMaskPixelToLocal(doc.Geotiff.GeoTransform, terrain, s.MidX, s.MidY)
				sp.IsSpline = true
				sp.MidPt = roadMaskVec2{X: ctrl.X, Z: ctrl.Z}
			}
			segs[i] = sp
		}
		ed.edges = append(ed.edges, roadMaskEditEdge{ID: se.ID, NodeIDs: ids, Segs: segs})
		if se.ID > maxEdge {
			maxEdge = se.ID
		}
	}
	ed.nextNodeID = maxNode + 1
	ed.nextEdgeID = maxEdge + 1
	ed.selNodeID = -1
	ed.selEdgeID = -1
	ed.selSegIdx = -1
	ed.drawing = false
	ed.draftNodeIDs = nil
	ed.draftSegProps = nil
	ed.dirty = false
	return nil
}

type roadMaskSaveFile struct {
	Geotiff roadMaskGeoTIFF    `json:"geotiff"`
	Nodes   []roadMaskNode     `json:"nodes"`
	Edges   []roadMaskSaveEdge `json:"edges"`
}

type roadMaskSaveEdge struct {
	ID      int               `json:"id"`
	NodeIDs []int             `json:"node_ids"`
	Segs    []roadMaskSaveSeg `json:"segs"`
}

type roadMaskSaveSeg struct {
	IsSpline bool     `json:"is_spline"`
	Curb     string   `json:"curb"`
	MidX     *float64 `json:"mid_x,omitempty"`
	MidY     *float64 `json:"mid_y,omitempty"`
}

func (ed *roadMaskEditor) save(path string, terrain *terrainData) error {
	doc := ed.toSaveFile(terrain)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create road mask directory: %w", err)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode road mask: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write road mask: %w", err)
	}
	ed.path = path
	ed.dirty = false
	return nil
}

func (ed *roadMaskEditor) toRoadMaskFile(terrain *terrainData) *roadMaskFile {
	save := ed.toSaveFile(terrain)
	doc := &roadMaskFile{
		Geotiff: save.Geotiff,
		Nodes:   save.Nodes,
		Edges:   make([]roadMaskEdge, 0, len(save.Edges)),
	}
	for _, edge := range save.Edges {
		segs := make([]roadMaskSeg, 0, len(edge.Segs))
		for _, seg := range edge.Segs {
			rs := roadMaskSeg{IsSpline: seg.IsSpline, Curb: seg.Curb}
			if seg.MidX != nil {
				rs.MidX = *seg.MidX
			}
			if seg.MidY != nil {
				rs.MidY = *seg.MidY
			}
			segs = append(segs, rs)
		}
		doc.Edges = append(doc.Edges, roadMaskEdge{
			ID:      edge.ID,
			NodeIDs: append([]int(nil), edge.NodeIDs...),
			Segs:    segs,
		})
	}
	return doc
}

func (ed *roadMaskEditor) toSaveFile(terrain *terrainData) roadMaskSaveFile {
	geo := ed.geo
	if geo.GeoTransform[1] == 0 && geo.GeoTransform[5] == 0 {
		geo = syntheticRoadMaskGeo(terrain)
	}
	doc := roadMaskSaveFile{
		Geotiff: geo,
		Nodes:   make([]roadMaskNode, 0, len(ed.nodes)),
		Edges:   make([]roadMaskSaveEdge, 0, len(ed.edges)),
	}
	for _, n := range ed.nodes {
		px, py := roadMaskLocalToPixel(geo.GeoTransform, terrain, n.Pos)
		doc.Nodes = append(doc.Nodes, roadMaskNode{ID: n.ID, X: px, Y: py})
	}
	for _, e := range ed.edges {
		segs := make([]roadMaskSaveSeg, len(e.Segs))
		for i, s := range e.Segs {
			seg := roadMaskSaveSeg{IsSpline: s.IsSpline, Curb: s.Curb.String()}
			if s.IsSpline {
				mx, my := roadMaskLocalToPixel(geo.GeoTransform, terrain, s.MidPt)
				seg.MidX = &mx
				seg.MidY = &my
			}
			segs[i] = seg
		}
		doc.Edges = append(doc.Edges, roadMaskSaveEdge{
			ID:      e.ID,
			NodeIDs: append([]int(nil), e.NodeIDs...),
			Segs:    segs,
		})
	}
	return doc
}

func roadMaskLocalToPixel(gt [6]float64, terrain *terrainData, p roadMaskVec2) (float64, float64) {
	if terrain == nil {
		return float64(p.X), float64(p.Z)
	}
	worldX := terrain.centerWorldX + float64(p.X)
	worldY := terrain.centerWorldY - float64(p.Z)
	det := gt[1]*gt[5] - gt[2]*gt[4]
	if math.Abs(det) <= 1e-12 {
		return float64(p.X), float64(p.Z)
	}
	dx := worldX - gt[0]
	dy := worldY - gt[3]
	px := (dx*gt[5] - gt[2]*dy) / det
	py := (gt[1]*dy - dx*gt[4]) / det
	return px, py
}

func syntheticRoadMaskGeo(terrain *terrainData) roadMaskGeoTIFF {
	geo := roadMaskGeoTIFF{
		GeoTransform: [6]float64{0, 1, 0, 0, 0, -1},
		PixelCoords:  "nodes and spline midpoints use synthetic 1m map pixels; map_x = gt[0] + pixel_x*gt[1] + pixel_y*gt[2], map_y = gt[3] + pixel_x*gt[4] + pixel_y*gt[5]",
	}
	if terrain == nil {
		return geo
	}
	geo.Width = int(math.Ceil(terrain.worldEast - terrain.worldWest))
	geo.Height = int(math.Ceil(terrain.worldNorth - terrain.worldSouth))
	geo.GeoTransform = [6]float64{terrain.worldWest, 1, 0, terrain.worldNorth, 0, -1}
	geo.Corners = &roadMaskCorners{
		UpperLeft:  []float64{terrain.worldWest, terrain.worldNorth},
		LowerLeft:  []float64{terrain.worldWest, terrain.worldSouth},
		LowerRight: []float64{terrain.worldEast, terrain.worldSouth},
		UpperRight: []float64{terrain.worldEast, terrain.worldNorth},
		Center:     []float64{terrain.centerWorldX, terrain.centerWorldY},
	}
	return geo
}

func primaryRoadMaskPath(mapDef *mapDefinition) string {
	if mapDef == nil || mapDef.ManifestPath == "" {
		return ""
	}
	if mapDef.RoadMaskPath != "" {
		return mapDef.RoadMaskPath
	}
	return filepath.Join(filepath.Dir(mapDef.ManifestPath), "road_masks", "road_mask.json")
}

func ensureManifestHasRoadMask(mapDef *mapDefinition, maskPath string) error {
	if mapDef == nil || mapDef.ManifestPath == "" {
		return fmt.Errorf("map manifest is not loaded")
	}
	file, err := os.Open(mapDef.ManifestPath)
	if err != nil {
		return fmt.Errorf("open map manifest for road mask update: %w", err)
	}
	var manifest mapManifest
	decodeErr := json.NewDecoder(file).Decode(&manifest)
	closeErr := file.Close()
	if decodeErr != nil {
		return fmt.Errorf("decode map manifest for road mask update: %w", decodeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close map manifest: %w", closeErr)
	}
	baseDir := filepath.Dir(mapDef.ManifestPath)
	rel, err := filepath.Rel(baseDir, maskPath)
	if err != nil {
		return fmt.Errorf("make road mask path relative: %w", err)
	}
	manifest.RoadMask = filepath.ToSlash(rel)
	manifest.RoadMasks = nil

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode map manifest with road mask: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(mapDef.ManifestPath, data, 0o644); err != nil {
		return fmt.Errorf("write map manifest with road mask: %w", err)
	}
	return nil
}

func (ed *roadMaskEditor) hasContent() bool {
	return len(ed.nodes) > 0 || len(ed.edges) > 0
}

func (ed *roadMaskEditor) closedEdgeCount() int {
	count := 0
	for i := range ed.edges {
		if roadMaskEditEdgeClosed(&ed.edges[i]) {
			count++
		}
	}
	return count
}

func (ed *roadMaskEditor) toolName() string {
	switch ed.tool {
	case roadMaskToolDraw:
		return "draw"
	case roadMaskToolEdit:
		return "edit"
	case roadMaskToolDelete:
		return "delete"
	case roadMaskToolCut:
		return "cut"
	default:
		return "unknown"
	}
}

func (ed *roadMaskEditor) helpText() string {
	switch ed.tool {
	case roadMaskToolDraw:
		if ed.splinePickingMid {
			return "MASK | LMB bend | RMB undo | Enter finish | Esc cancel control point"
		}
		if ed.drawing {
			return "MASK | LMB endpoint | Z/L line | C spline | H hard | F soft | aim start to close | RMB undo | Enter finish | Esc cancel"
		}
		return "MASK | LMB start | G draw | V edit | R delete | X cut | Z/L line | C spline | H hard | F soft | Tab pointer | Ctrl+S save | F4 exit"
	case roadMaskToolEdit:
		return "MASK EDIT | LMB select/drag node or spline handle | Z/L line | C spline | H hard | F soft | Delete straighten/remove | Tab pointer | F4 exit"
	case roadMaskToolDelete:
		return "MASK DELETE | LMB node or edge | G draw | V edit | X cut | Tab pointer | Ctrl+S save | F4 exit"
	case roadMaskToolCut:
		return "MASK CUT | LMB edge to split | G draw | V edit | R delete | Tab pointer | Ctrl+S save | F4 exit"
	default:
		return "MASK | F4 exit"
	}
}

func roadMaskPolygonArea(pts []roadMaskVec2) float32 {
	var area float32
	for i, p := range pts {
		q := pts[(i+1)%len(pts)]
		area += p.X*q.Z - q.X*p.Z
	}
	return area * 0.5
}

func roadMaskCross(a, b, c roadMaskVec2) float32 {
	ab := b.Sub(a)
	ac := c.Sub(a)
	return ab.X*ac.Z - ab.Z*ac.X
}

func roadMaskIsConvex(a, b, c roadMaskVec2, orientation float32) bool {
	return roadMaskCross(a, b, c)*orientation > 1e-5
}

func roadMaskPointStrictlyInTriangle(p, a, b, c roadMaskVec2) bool {
	const eps float32 = 1e-5
	c1 := roadMaskCross(a, b, p)
	c2 := roadMaskCross(b, c, p)
	c3 := roadMaskCross(c, a, p)
	return (c1 > eps && c2 > eps && c3 > eps) || (c1 < -eps && c2 < -eps && c3 < -eps)
}

func roadMaskTriangleContainsAnyPoint(poly []roadMaskVec2, indices []int, a, b, c int) bool {
	for _, idx := range indices {
		if idx == a || idx == b || idx == c {
			continue
		}
		if roadMaskPointStrictlyInTriangle(poly[idx], poly[a], poly[b], poly[c]) {
			return true
		}
	}
	return false
}

func roadMaskPolygonVertices(pts []roadMaskVec2) []roadMaskVec2 {
	out := make([]roadMaskVec2, 0, len(pts))
	for _, p := range pts {
		if len(out) == 0 || roadMaskDist2(out[len(out)-1], p) > 1e-4 {
			out = append(out, p)
		}
	}
	if len(out) > 1 && roadMaskDist2(out[0], out[len(out)-1]) <= 1e-4 {
		out = out[:len(out)-1]
	}
	return out
}

func triangulateRoadMaskPolygon(pts []roadMaskVec2) [][3]roadMaskVec2 {
	poly := roadMaskPolygonVertices(pts)
	if len(poly) < 3 {
		return nil
	}
	orientation := float32(1)
	if roadMaskPolygonArea(poly) < 0 {
		orientation = -1
	}

	indices := make([]int, len(poly))
	for i := range indices {
		indices[i] = i
	}

	tris := make([][3]roadMaskVec2, 0, len(poly)-2)
	guard := len(indices) * len(indices)
	for len(indices) > 3 && guard > 0 {
		guard--
		earFound := false
		for i := range indices {
			prev := indices[(i+len(indices)-1)%len(indices)]
			curr := indices[i]
			next := indices[(i+1)%len(indices)]
			if !roadMaskIsConvex(poly[prev], poly[curr], poly[next], orientation) {
				continue
			}
			if roadMaskTriangleContainsAnyPoint(poly, indices, prev, curr, next) {
				continue
			}
			tris = append(tris, [3]roadMaskVec2{poly[prev], poly[curr], poly[next]})
			indices = append(indices[:i], indices[i+1:]...)
			earFound = true
			break
		}
		if !earFound {
			return tris
		}
	}
	if len(indices) == 3 {
		tris = append(tris, [3]roadMaskVec2{poly[indices[0]], poly[indices[1]], poly[indices[2]]})
	}
	return tris
}
