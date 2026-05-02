//go:build !darwin

package main

import (
	"errors"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	simpkg "github.com/errornesttorn/mini-traffic-simulation-core"
	rl "github.com/gen2brain/raylib-go/raylib"
)

const (
	screenW    = 1280
	screenH    = 720
	moveSpeed  = 40.0
	sprintMult = 6.0
	mouseSens  = 0.2
	groundY    = 0.0

	carHeight     = 1.5
	busHeight     = 3.2
	poleHeight    = 3.0
	lightHeadSize = 0.45
	pedHeight     = 1.75
	pedWidth      = 0.5

	terrainMeshMaxDim    = 2048
	terrainTextureMaxDim = 8192
)

type App struct {
	world                      *simpkg.World
	loaded                     bool
	paused                     bool
	showPaths                  bool
	terrain                    *terrainData
	objects                    *sceneObjects
	mapDef                     *mapDefinition
	mapName                    string
	camPos                     rl.Vector3
	yaw                        float32
	pitch                      float32
	mouseCaptured              bool
	unitCube                   rl.Model
	loader                     *mapLoader
	showVRAM                   bool
	editMode                   bool
	propTool                   propEditTool
	selectedProp               int
	selectedLinearProp         int
	availablePropAssets        []string
	currentPropAsset           string
	currentLinearAsset         string
	currentPropHeading         float32
	currentLinearHeadingOffset float32
	currentPropScale           float32
	currentLinearScale         float32
	currentLinearSpacing       float32
	linearDraft                []linearPropPoint
	draggingProp               bool
	propDirty                  bool
	propStatus                 string
	propStatusUntil            float64
}

type loaderPhase int

const (
	loaderPhaseCPU loaderPhase = iota
	loaderPhaseTerrain
	loaderPhaseTrees
	loaderPhaseDone
)

type mapLoader struct {
	statusMu sync.Mutex
	status   string

	cpuDone atomic.Bool
	cpuErr  atomic.Pointer[error]
	mapDef  *mapDefinition
	terrain *terrainCPUData
	scene   *sceneCPUData

	phase       loaderPhase
	terrainData *terrainData
	foliage     treeFoliageResources
	problems    []error
}

func (l *mapLoader) setStatus(s string) {
	l.statusMu.Lock()
	l.status = s
	l.statusMu.Unlock()
}

func (l *mapLoader) getStatus() string {
	l.statusMu.Lock()
	defer l.statusMu.Unlock()
	return l.status
}

func (l *mapLoader) progress() (float32, string) {
	if l == nil {
		return 0, ""
	}
	switch l.phase {
	case loaderPhaseCPU:
		return 0.10, l.getStatus()
	case loaderPhaseTerrain:
		return 0.65, "uploading terrain"
	case loaderPhaseTrees:
		return 0.92, "uploading trees"
	default:
		return 1, "done"
	}
}

func main() {
	rl.SetConfigFlags(rl.FlagWindowResizable | rl.FlagMsaa4xHint)
	rl.InitWindow(screenW, screenH, "Warsaw Commute - Driving Game")
	defer rl.CloseWindow()
	rl.SetTargetFPS(60)
	rl.DisableCursor()

	app := &App{
		camPos:               rl.NewVector3(0, 10, 0),
		pitch:                -20,
		mouseCaptured:        true,
		showPaths:            true,
		unitCube:             rl.LoadModelFromMesh(rl.GenMeshCube(1, 1, 1)),
		selectedProp:         -1,
		selectedLinearProp:   -1,
		currentPropAsset:     defaultPropAsset,
		currentLinearAsset:   defaultLinearPropAsset,
		currentPropScale:     1,
		currentLinearScale:   1,
		currentLinearSpacing: defaultLinearSpacingM,
	}
	defer rl.UnloadModel(app.unitCube)
	defer func() { unloadTerrain(app.terrain) }()
	defer func() { unloadSceneObjects(app.objects) }()

	for !rl.WindowShouldClose() {
		app.update()
		app.draw()
	}
}

// ---------- update ----------

func (a *App) update() {
	dt := rl.GetFrameTime()

	if rl.IsKeyPressed(rl.KeyEscape) {
		rl.CloseWindow()
	}

	if a.loader != nil {
		a.advanceLoader()
		return
	}

	if rl.IsKeyPressed(rl.KeyTab) && !a.editMode {
		a.mouseCaptured = !a.mouseCaptured
		if a.mouseCaptured {
			rl.DisableCursor()
		} else {
			rl.EnableCursor()
		}
	}

	if (rl.IsKeyDown(rl.KeyLeftControl) || rl.IsKeyDown(rl.KeyRightControl)) && rl.IsKeyPressed(rl.KeyO) {
		a.openMap()
	}

	if rl.IsKeyPressed(rl.KeySpace) {
		a.paused = !a.paused
	}

	if rl.IsKeyPressed(rl.KeyP) {
		a.showPaths = !a.showPaths
	}

	if rl.IsKeyPressed(rl.KeyF3) {
		a.showVRAM = !a.showVRAM
	}

	if rl.IsKeyPressed(rl.KeyF2) {
		a.togglePropEditor()
	}

	if a.editMode {
		a.updatePropEditor()
	}

	if a.loaded && !a.paused {
		a.world.Step(dt)
	}

	if a.editMode && rl.IsMouseButtonDown(rl.MouseRightButton) {
		delta := rl.GetMouseDelta()
		a.yaw -= delta.X * mouseSens
		a.pitch -= delta.Y * mouseSens
		if a.pitch > 89 {
			a.pitch = 89
		}
		if a.pitch < -89 {
			a.pitch = -89
		}
	} else if a.mouseCaptured {
		delta := rl.GetMouseDelta()
		a.yaw -= delta.X * mouseSens
		a.pitch -= delta.Y * mouseSens
		if a.pitch > 89 {
			a.pitch = 89
		}
		if a.pitch < -89 {
			a.pitch = -89
		}
	}

	yawRad := float64(a.yaw) * math.Pi / 180.0
	fwdX := float32(math.Sin(yawRad))
	fwdZ := float32(math.Cos(yawRad))
	fwd := rl.NewVector3(fwdX, 0, fwdZ)
	right := rl.NewVector3(fwdZ, 0, -fwdX)

	speed := float32(moveSpeed)
	if rl.IsKeyDown(rl.KeyLeftShift) || rl.IsKeyDown(rl.KeyRightShift) {
		speed *= sprintMult
	}

	if rl.IsKeyDown(rl.KeyW) {
		a.camPos = addVec3(a.camPos, scaleVec3(fwd, speed*dt))
	}
	ctrlDown := rl.IsKeyDown(rl.KeyLeftControl) || rl.IsKeyDown(rl.KeyRightControl)
	if rl.IsKeyDown(rl.KeyS) && !(a.editMode && ctrlDown) {
		a.camPos = addVec3(a.camPos, scaleVec3(fwd, -speed*dt))
	}
	if rl.IsKeyDown(rl.KeyA) {
		a.camPos = addVec3(a.camPos, scaleVec3(right, speed*dt))
	}
	if rl.IsKeyDown(rl.KeyD) {
		a.camPos = addVec3(a.camPos, scaleVec3(right, -speed*dt))
	}
	if rl.IsKeyDown(rl.KeyE) {
		a.camPos.Y += speed * dt
	}
	if rl.IsKeyDown(rl.KeyQ) {
		a.camPos.Y -= speed * dt
	}

	pumpTerrainStreaming(a.terrain, a.camPos.X, a.camPos.Z)
	pumpBuildingStreaming(a.objects, a.camPos.X, a.camPos.Z)
}

// ---------- draw ----------

func (a *App) draw() {
	rl.BeginDrawing()
	rl.ClearBackground(rl.NewColor(170, 208, 253, 255))

	if a.loader != nil {
		a.drawLoadingScreen()
		rl.EndDrawing()
		return
	}

	camera := a.buildCamera()
	rl.SetClipPlanes(0.01, 2000.0)
	rl.BeginMode3D(camera)
	if a.terrain != nil {
		drawTerrainWithRoadCuts(a.terrain)
		drawRoadSurfaceLayer(a.terrain)
		drawSceneObjects(camera, a.terrain, a.objects)
	} else {
		rl.DrawGrid(200, 1.0)
	}

	if a.loaded {
		if a.showPaths {
			a.drawPedestrianPaths()
			a.drawSplines()
		}
		a.drawTrafficLights()
		a.drawCars()
		a.drawPedestrians()
	}
	if a.editMode {
		a.drawPropEditor3D(camera)
	}

	rl.EndMode3D()
	a.drawHUD()
	rl.EndDrawing()
}

func (a *App) drawLoadingScreen() {
	w := int32(rl.GetScreenWidth())
	h := int32(rl.GetScreenHeight())

	frac, status := a.loader.progress()
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}

	title := "Loading map…"
	if a.loader.mapDef != nil {
		title = fmt.Sprintf("Loading %s…", a.loader.mapDef.Name)
	}
	titleSize := int32(28)
	tw := rl.MeasureText(title, titleSize)
	rl.DrawText(title, w/2-tw/2, h/2-80, titleSize, rl.White)

	barW := int32(420)
	if barW > w-80 {
		barW = w - 80
	}
	barH := int32(22)
	barX := w/2 - barW/2
	barY := h/2 - barH/2

	rl.DrawRectangle(barX, barY, barW, barH, rl.NewColor(50, 50, 60, 255))
	rl.DrawRectangle(barX, barY, int32(float32(barW)*frac), barH, rl.NewColor(120, 200, 120, 255))
	rl.DrawRectangleLines(barX, barY, barW, barH, rl.LightGray)

	pct := fmt.Sprintf("%d%%", int(frac*100))
	pw := rl.MeasureText(pct, 18)
	rl.DrawText(pct, w/2-pw/2, barY+barH+12, 18, rl.White)

	if status != "" {
		sw := rl.MeasureText(status, 16)
		rl.DrawText(status, w/2-sw/2, barY+barH+38, 16, rl.LightGray)
	}
}

func (a *App) buildCamera() rl.Camera3D {
	yawRad := float64(a.yaw) * math.Pi / 180.0
	pitchRad := float64(a.pitch) * math.Pi / 180.0
	dirX := float32(math.Cos(pitchRad) * math.Sin(yawRad))
	dirY := float32(math.Sin(pitchRad))
	dirZ := float32(math.Cos(pitchRad) * math.Cos(yawRad))
	return rl.Camera3D{
		Position:   a.camPos,
		Target:     rl.NewVector3(a.camPos.X+dirX, a.camPos.Y+dirY, a.camPos.Z+dirZ),
		Up:         rl.NewVector3(0, 1, 0),
		Fovy:       70,
		Projection: rl.CameraPerspective,
	}
}

func (a *App) drawSplines() {
	for _, s := range a.world.Splines {
		col := rl.Yellow
		if s.BusOnly {
			col = rl.NewColor(80, 160, 255, 255)
		}
		prev := a.sim2world(s.Samples[0])
		prev.Y += 0.05
		for i := 1; i < len(s.Samples); i++ {
			cur := a.sim2world(s.Samples[i])
			cur.Y += 0.05
			rl.DrawLine3D(prev, cur, col)
			prev = cur
		}
	}
}

func (a *App) drawPedestrianPaths() {
	col := rl.NewColor(180, 180, 180, 180)
	for _, p := range a.world.PedestrianPaths {
		dx := p.P1.X - p.P0.X
		dz := p.P1.Y - p.P0.Y
		length := float32(math.Sqrt(float64(dx*dx + dz*dz)))
		if length < 0.01 {
			continue
		}
		angle := float32(math.Atan2(float64(dx), float64(dz))) * 180 / math.Pi
		cx := (p.P0.X + p.P1.X) / 2
		cz := (p.P0.Y + p.P1.Y) / 2
		size := rl.NewVector3(simpkg.PedestrianPathWidthM, 0.04, length)
		a.drawBox(rl.NewVector3(cx, a.groundAt(cx, cz)+0.02, cz), size, angle, col)
	}
}

func (a *App) drawTrafficLights() {
	for _, light := range a.world.TrafficLights {
		px := light.WorldPos.X
		pz := light.WorldPos.Y

		ground := a.groundAt(px, pz)
		// Pole
		poleCenter := rl.NewVector3(px, ground+poleHeight/2, pz)
		rl.DrawCubeV(poleCenter, rl.NewVector3(0.15, poleHeight, 0.15), rl.DarkGray)

		// Light head
		headY := float32(ground + poleHeight + lightHeadSize/2)
		col := a.trafficLightColor(light)
		rl.DrawCubeV(rl.NewVector3(px, headY, pz), rl.NewVector3(lightHeadSize, lightHeadSize, lightHeadSize), col)
	}
}

func (a *App) drawCars() {
	allSplines := simpkg.MergedSplines(a.world.Splines, a.world.LaneChangeSplines)
	blinkOn := int(rl.GetTime()*2)%2 == 0
	amber := rl.NewColor(255, 165, 0, 255)

	for _, car := range a.world.Cars {
		frontPos, center, heading, ok := carBodyPose(car, allSplines)
		if !ok {
			continue
		}

		cx, cz := center.X, center.Y
		hx, hz := heading.X, heading.Y
		angle := float32(math.Atan2(float64(hx), float64(hz))) * 180 / math.Pi

		h := float32(carHeight)
		if car.VehicleKind == simpkg.VehicleBus {
			h = busHeight
		}
		length := car.Length
		width := car.Width

		ground := a.groundAt(cx, cz)
		pitchDeg, rollDeg := a.terrainTilt(cx, cz, hx, hz, length, width)

		c := car.Color
		a.drawOrientedBox(
			rl.NewVector3(cx, ground+h/2, cz),
			rl.NewVector3(width, h, length),
			angle, pitchDeg, rollDeg,
			rl.NewColor(c.R, c.G, c.B, 255),
		)

		// Trailer
		if car.Trailer.HasTrailer {
			rearX, rearZ := car.RearPosition.X, car.RearPosition.Y
			trX, trZ := car.Trailer.RearPosition.X, car.Trailer.RearPosition.Y
			tdx := rearX - trX
			tdz := rearZ - trZ
			trailerAngle := float32(math.Atan2(float64(tdx), float64(tdz))) * 180 / math.Pi
			thx := float32(math.Sin(float64(trailerAngle) * math.Pi / 180))
			thz := float32(math.Cos(float64(trailerAngle) * math.Pi / 180))
			tcx := (rearX + trX) / 2
			tcz := (rearZ + trZ) / 2
			tGround := a.groundAt(tcx, tcz)
			tPitch, tRoll := a.terrainTilt(tcx, tcz, thx, thz, car.Trailer.Length, car.Trailer.Width)
			trailerCenter := rl.NewVector3(tcx, tGround+h/2, tcz)
			tc := car.Trailer.Color
			a.drawOrientedBox(
				trailerCenter,
				rl.NewVector3(car.Trailer.Width, h, car.Trailer.Length),
				trailerAngle, tPitch, tRoll,
				rl.NewColor(tc.R, tc.G, tc.B, 255),
			)
		}

		// Turn signal indicators (front corners)
		if blinkOn && car.TurnSignal != simpkg.TurnSignalNone {
			// right-of-car in 3D: perpendicular clockwise to heading
			// heading (hx,hz) → right = (hz, 0, -hx) in world
			rx, rz := hz, -hx
			halfWid := width/2 + 0.05
			indSize := rl.NewVector3(0.25, 0.25, 0.05)
			indY := ground + h*0.7

			if car.TurnSignal == simpkg.TurnSignalLeft {
				// left = -right
				ix := frontPos.X - hx*0.2 + (-rx)*halfWid
				iz := frontPos.Y - hz*0.2 + (-rz)*halfWid
				a.drawOrientedBox(rl.NewVector3(ix, indY, iz), indSize, angle, pitchDeg, rollDeg, amber)
			} else {
				ix := frontPos.X - hx*0.2 + rx*halfWid
				iz := frontPos.Y - hz*0.2 + rz*halfWid
				a.drawOrientedBox(rl.NewVector3(ix, indY, iz), indSize, angle, pitchDeg, rollDeg, amber)
			}
		}
	}
}

func carBodyPose(car simpkg.Car, splines []simpkg.Spline) (frontPos, center, heading simpkg.Vec2, ok bool) {
	spline, ok := simpkg.FindSplineByID(splines, car.CurrentSplineID)
	if !ok {
		return simpkg.Vec2{}, simpkg.Vec2{}, simpkg.Vec2{}, false
	}
	splinePos, splineTangent := simpkg.SampleSplineAtDistance(spline, car.DistanceOnSpline)
	rightNormal := simpkg.Vec2{X: splineTangent.Y, Y: -splineTangent.X}
	frontPos = vec2Add(splinePos, vec2Scale(rightNormal, car.LateralOffset))
	heading = vec2Normalize(vec2Sub(frontPos, car.RearPosition))
	if vec2LengthSq(vec2Sub(frontPos, car.RearPosition)) <= 1e-9 {
		heading = splineTangent
	}
	center = vec2Scale(vec2Add(frontPos, car.RearPosition), 0.5)
	return frontPos, center, heading, true
}

func vec2Add(a, b simpkg.Vec2) simpkg.Vec2 {
	return simpkg.Vec2{X: a.X + b.X, Y: a.Y + b.Y}
}

func vec2Sub(a, b simpkg.Vec2) simpkg.Vec2 {
	return simpkg.Vec2{X: a.X - b.X, Y: a.Y - b.Y}
}

func vec2Scale(v simpkg.Vec2, s float32) simpkg.Vec2 {
	return simpkg.Vec2{X: v.X * s, Y: v.Y * s}
}

func vec2LengthSq(v simpkg.Vec2) float32 {
	return v.X*v.X + v.Y*v.Y
}

func vec2Normalize(v simpkg.Vec2) simpkg.Vec2 {
	lenSq := vec2LengthSq(v)
	if lenSq <= 1e-9 {
		return simpkg.Vec2{}
	}
	inv := 1 / float32(math.Sqrt(float64(lenSq)))
	return vec2Scale(v, inv)
}

func (a *App) drawPedestrians() {
	skinColor := rl.NewColor(220, 180, 140, 255)

	for _, ped := range a.world.Pedestrians {
		var px, pz float32

		if ped.TransitionActive {
			// Quadratic bezier along the crossing arc
			t := ped.TransitionDistance / ped.TransitionLength
			if t > 1 {
				t = 1
			}
			t1 := 1 - t
			px = t1*t1*ped.TransitionP0.X + 2*t*t1*ped.TransitionP1.X + t*t*ped.TransitionP2.X
			pz = t1*t1*ped.TransitionP0.Y + 2*t*t1*ped.TransitionP1.Y + t*t*ped.TransitionP2.Y
		} else {
			if ped.PathIndex < 0 || ped.PathIndex >= len(a.world.PedestrianPaths) {
				continue
			}
			path := a.world.PedestrianPaths[ped.PathIndex]
			dx := path.P1.X - path.P0.X
			dz := path.P1.Y - path.P0.Y
			pathLen := float32(math.Sqrt(float64(dx*dx + dz*dz)))
			if pathLen < 0.01 {
				continue
			}
			ndx, ndz := dx/pathLen, dz/pathLen
			// perpendicular (left of direction)
			perpX, perpZ := -ndz, ndx
			px = path.P0.X + ndx*ped.Distance + perpX*ped.LateralOffset
			pz = path.P0.Y + ndz*ped.Distance + perpZ*ped.LateralOffset
		}

		center := rl.NewVector3(px, a.groundAt(px, pz)+pedHeight/2, pz)
		rl.DrawCubeV(center, rl.NewVector3(pedWidth, pedHeight, pedWidth*0.8), skinColor)
	}
}

// ---------- HUD ----------

func (a *App) drawHUD() {
	w := int32(rl.GetScreenWidth())
	h := int32(rl.GetScreenHeight())

	if a.terrain == nil {
		msg := "Press Ctrl+O to open a map folder (map.json)"
		tw := rl.MeasureText(msg, 20)
		rl.DrawText(msg, w/2-tw/2, h/2-10, 20, rl.White)
	} else if !a.loaded {
		msg := fmt.Sprintf("Map %q loaded — no simulation referenced", a.mapName)
		tw := rl.MeasureText(msg, 20)
		rl.DrawText(msg, w/2-tw/2, h/2-10, 20, rl.White)
	}

	cx, cy := w/2, h/2
	rl.DrawLine(cx-10, cy, cx+10, cy, rl.White)
	rl.DrawLine(cx, cy-10, cx, cy+10, rl.White)

	helpText := "TAB: toggle mouse | Ctrl+O: open | WASD+E/Q: fly | Space: pause | P: paths | F2: props | F3: vram | Shift: sprint"
	if a.editMode {
		helpText = "PROP EDIT | click asset | 1 prop | 2 select | 3 linear | LMB action | Enter commit line | MMB rotate | ,/. spacing | Ctrl+S save"
	}
	rl.DrawText(helpText, 8, h-24, 14, rl.LightGray)

	if a.paused {
		msg := "PAUSED"
		tw := rl.MeasureText(msg, 30)
		rl.DrawText(msg, w/2-tw/2, h/2-60, 30, rl.Red)
	}

	if a.loaded {
		buildings := 0
		totalRegions := 0
		residentRegions := 0
		inFlightRegions := 0
		upgradingRegions := 0
		trees := 0
		if a.objects != nil {
			buildings = a.objects.BuildingCount
			totalRegions = len(a.objects.BuildingRegions)
			residentRegions, inFlightRegions, upgradingRegions = residentBuildingRegions(a.objects)
			trees = len(a.objects.Trees)
		}
		props := 0
		if a.objects != nil {
			props = len(a.objects.Props)
		}
		info := fmt.Sprintf("Cars: %d  Splines: %d  Peds: %d  Buildings: %d  GLB: %d/%d (+%d loading, %d upgrading)  Trees: %d  Props: %d  Pos: (%.1f, %.1f, %.1f)",
			len(a.world.Cars), len(a.world.Splines), len(a.world.Pedestrians),
			buildings, residentRegions, totalRegions, inFlightRegions, upgradingRegions, trees, props,
			a.camPos.X, a.camPos.Y, a.camPos.Z)
		rl.DrawText(info, 8, 8, 16, rl.White)
	}

	if a.editMode {
		a.drawPropEditorHUD(w, h)
	} else if !a.mouseCaptured {
		msg := "MOUSE RELEASED - press TAB to recapture"
		rl.DrawText(msg, w/2-int32(rl.MeasureText(msg, 16))/2, 8, 16, rl.Orange)
	}

	if a.showVRAM {
		drawVRAMProfiler(a)
	}
}

// ---------- helpers ----------

func (a *App) drawBox(pos, size rl.Vector3, yawDeg float32, color rl.Color) {
	rl.DrawModelEx(a.unitCube, pos, rl.NewVector3(0, 1, 0), yawDeg, size, color)
}

// drawOrientedBox draws the unit cube with yaw + pitch + roll (degrees).
// Pitch rotates around the car's right axis (nose up/down), roll around
// the forward axis (lateral tilt). Falls back to the simple path when there
// is no tilt.
func (a *App) drawOrientedBox(pos, size rl.Vector3, yawDeg, pitchDeg, rollDeg float32, tint rl.Color) {
	if pitchDeg == 0 && rollDeg == 0 {
		a.drawBox(pos, size, yawDeg, tint)
		return
	}

	const deg2rad = float32(math.Pi / 180.0)
	scaleM := rl.MatrixScale(size.X, size.Y, size.Z)
	rollM := rl.MatrixRotate(rl.NewVector3(0, 0, 1), rollDeg*deg2rad)
	pitchM := rl.MatrixRotate(rl.NewVector3(1, 0, 0), pitchDeg*deg2rad)
	yawM := rl.MatrixRotate(rl.NewVector3(0, 1, 0), yawDeg*deg2rad)
	transM := rl.MatrixTranslate(pos.X, pos.Y, pos.Z)

	// raylib's MatrixMultiply(A, B) returns B*A in math, so each call stacks
	// the next step on the LEFT. Desired application order on a local vertex
	// (innermost first): scale, roll, pitch, yaw, translate.
	m := scaleM
	m = rl.MatrixMultiply(m, rollM)
	m = rl.MatrixMultiply(m, pitchM)
	m = rl.MatrixMultiply(m, yawM)
	m = rl.MatrixMultiply(m, transM)

	meshes := a.unitCube.GetMeshes()
	materials := a.unitCube.GetMaterials()
	if len(meshes) == 0 || len(materials) == 0 {
		return
	}
	mapPtr := materials[0].GetMap(int32(rl.MapAlbedo))
	old := mapPtr.Color
	mapPtr.Color = tint
	rl.DrawMesh(meshes[0], materials[0], m)
	mapPtr.Color = old
}

// terrainTilt returns (pitchDeg, rollDeg) so a box centred at (cx,cz) with the
// given heading and footprint conforms to the terrain surface. Returns (0,0)
// when no terrain is loaded.
func (a *App) terrainTilt(cx, cz, hx, hz, length, width float32) (float32, float32) {
	if a.terrain == nil {
		return 0, 0
	}
	halfLen := length * 0.4
	halfWid := width * 0.4
	if halfLen < 0.2 {
		halfLen = 0.2
	}
	if halfWid < 0.2 {
		halfWid = 0.2
	}

	hFront := terrainHeightAtLocal(a.terrain, cx+hx*halfLen, cz+hz*halfLen)
	hBack := terrainHeightAtLocal(a.terrain, cx-hx*halfLen, cz-hz*halfLen)
	// right-of-heading in XZ (clockwise from above)
	rx, rz := hz, -hx
	hRight := terrainHeightAtLocal(a.terrain, cx+rx*halfWid, cz+rz*halfWid)
	hLeft := terrainHeightAtLocal(a.terrain, cx-rx*halfWid, cz-rz*halfWid)

	// Pitch: +θ around world +X sends local +Z to -Y (right-hand rule), so
	// negate to make positive pitch = nose up.
	pitch := -math.Atan2(float64(hFront-hBack), float64(2*halfLen))
	// Roll: +θ around world +Z sends local +X to +Y, so positive roll = right
	// side up when the terrain rises to the right.
	roll := math.Atan2(float64(hRight-hLeft), float64(2*halfWid))
	return float32(pitch * 180 / math.Pi), float32(roll * 180 / math.Pi)
}

func (a *App) trafficLightColor(light simpkg.TrafficLight) rl.Color {
	for i := range a.world.TrafficCycles {
		c := &a.world.TrafficCycles[i]
		if c.ID != light.CycleID || !c.Enabled || len(c.Phases) == 0 {
			continue
		}
		if c.PhaseIndex < 0 || c.PhaseIndex >= len(c.Phases) {
			return rl.Red
		}
		phase := c.Phases[c.PhaseIndex]
		for _, id := range phase.GreenLightIDs {
			if id == light.ID {
				if c.Timer <= phase.DurationSecs {
					return rl.Green
				}
				return rl.Yellow
			}
		}
		return rl.Red
	}
	return rl.DarkGray
}

func (a *App) openMap() {
	if a.loader != nil {
		return
	}
	rl.EnableCursor()
	path, err := pickMapPath()
	rl.DisableCursor()
	a.mouseCaptured = true
	if err != nil || path == "" {
		return
	}

	mapDef, err := loadMapDefinition(path)
	if err != nil {
		fmt.Printf("Failed to load map: %v\n", err)
		return
	}

	loader := &mapLoader{mapDef: mapDef, phase: loaderPhaseCPU}
	loader.setStatus("preparing terrain")
	a.loader = loader

	go func() {
		terrainCPU, err := prepareTerrainCPU(mapDef, terrainMeshMaxDim, terrainTextureMaxDim)
		if err != nil {
			e := fmt.Errorf("build terrain: %w", err)
			loader.cpuErr.Store(&e)
			loader.cpuDone.Store(true)
			return
		}
		fakeTerrain := terrainForSceneCPU(terrainCPU)
		loader.setStatus("loading road surfaces")
		roadCPU, roadProblems := prepareRoadSurfacesCPU(mapDef, fakeTerrain)
		terrainCPU.roads = roadCPU
		fakeTerrain.roads = roadHeightOnlyLayer(roadCPU)
		scene := prepareSceneCPU(mapDef, fakeTerrain, loader.setStatus)
		scene.Problems = append(scene.Problems, roadProblems...)
		loader.terrain = terrainCPU
		loader.scene = scene
		loader.cpuDone.Store(true)
	}()
}

func terrainForSceneCPU(cpu *terrainCPUData) *terrainData {
	source := cpu.source
	return &terrainData{
		heightSamples: source.heights,
		position: rl.NewVector3(
			float32(source.worldWest-source.centerX),
			float32(source.minHeight-source.centerZ),
			float32(source.centerY-source.worldNorth),
		),
		centerWorldX: source.centerX,
		centerWorldY: source.centerY,
		centerWorldZ: source.centerZ,
		widthMeters:  cpu.widthMeters,
		depthMeters:  cpu.depthMeters,
		heightMeters: cpu.heightMeters,
		heightMin:    source.minHeight,
		heightMax:    source.maxHeight,
		meshWidth:    cpu.cropW,
		meshHeight:   cpu.cropH,
		textureWidth: cpu.textureW,
		worldWest:    source.worldWest,
		worldEast:    source.worldEast,
		worldSouth:   source.worldSouth,
		worldNorth:   source.worldNorth,
		roads:        roadHeightOnlyLayer(cpu.roads),
	}
}

func (a *App) advanceLoader() {
	l := a.loader
	if l == nil {
		return
	}

	switch l.phase {
	case loaderPhaseCPU:
		if !l.cpuDone.Load() {
			return
		}
		if errPtr := l.cpuErr.Load(); errPtr != nil {
			fmt.Printf("Failed to load map: %v\n", *errPtr)
			a.loader = nil
			return
		}
		l.phase = loaderPhaseTerrain

	case loaderPhaseTerrain:
		td, err := finishTerrainGPU(l.terrain)
		if err != nil {
			fmt.Printf("Failed to upload terrain: %v\n", err)
			a.loader = nil
			return
		}
		l.terrainData = td
		l.phase = loaderPhaseTrees

	case loaderPhaseTrees:
		if len(l.scene.Trees) > 0 && l.scene.FoliageAtlas != nil {
			l.foliage = uploadTreeFoliage(l.scene.FoliageAtlas, l.scene.Trees)
		}
		l.phase = loaderPhaseDone
		a.installLoadedMap()
	}
}

func (a *App) installLoadedMap() {
	l := a.loader
	if l == nil {
		return
	}

	totalBuildings := 0
	for i := range l.scene.Regions {
		totalBuildings += l.scene.Regions[i].BuildingCount
	}
	propAssets, propProblems := loadPropAssets(l.mapDef, l.scene.Props, l.scene.LinearProps)
	problems := append([]error(nil), l.scene.Problems...)
	problems = append(problems, propProblems...)
	objects := &sceneObjects{
		BuildingRegions: l.scene.Regions,
		BuildingCount:   totalBuildings,
		Trees:           l.scene.Trees,
		TreeFoliage:     l.foliage,
		Props:           l.scene.Props,
		LinearProps:     l.scene.LinearProps,
		PropAssets:      propAssets,
	}

	unloadTerrain(a.terrain)
	unloadSceneObjects(a.objects)
	a.terrain = l.terrainData
	a.objects = objects
	a.mapDef = l.mapDef
	a.mapName = l.mapDef.Name
	a.world = nil
	a.loaded = false
	a.editMode = false
	a.selectedProp = -1
	a.selectedLinearProp = -1
	a.availablePropAssets = discoverPropAssets(l.mapDef, l.scene.Props, l.scene.LinearProps)
	a.linearDraft = nil
	a.draggingProp = false
	a.propDirty = false

	if len(problems) > 0 {
		fmt.Printf("Map loaded, but scene object load had problems: %v\n", errors.Join(problems...))
	}

	if l.mapDef.Simulation != "" {
		simPath := filepath.Join(filepath.Dir(l.mapDef.ManifestPath), l.mapDef.Simulation)
		if w, err := simpkg.LoadWorld(simPath); err == nil {
			a.world = w
			a.loaded = true
		} else {
			fmt.Printf("Map loaded, but simulation %s failed: %v\n", l.mapDef.Simulation, err)
		}
	}

	cx := a.terrain.position.X + a.terrain.widthMeters*0.5
	cz := a.terrain.position.Z + a.terrain.depthMeters*0.5
	ground := terrainHeightAtLocal(a.terrain, cx, cz)
	a.camPos = rl.NewVector3(cx, ground+80, cz+a.terrain.depthMeters*0.4)
	a.pitch = -35
	a.yaw = 0

	a.loader = nil

	startTerrainStreaming(a.terrain)
	startBuildingStreaming(a.objects, a.camPos.X, a.camPos.Z)
}

func (a *App) sim2world(v simpkg.Vec2) rl.Vector3 {
	return rl.NewVector3(v.X, a.groundAt(v.X, v.Y), v.Y)
}

// groundAt returns the raylib Y height at sim (x, y) — i.e. raylib (x, z).
// When no terrain is loaded, this is groundY.
func (a *App) groundAt(simX, simY float32) float32 {
	if a.terrain == nil {
		return groundY
	}
	return terrainHeightAtLocal(a.terrain, simX, simY)
}

func addVec3(a, b rl.Vector3) rl.Vector3 {
	return rl.NewVector3(a.X+b.X, a.Y+b.Y, a.Z+b.Z)
}

func scaleVec3(v rl.Vector3, s float32) rl.Vector3 {
	return rl.NewVector3(v.X*s, v.Y*s, v.Z*s)
}

func pickMapPath() (string, error) {
	out, err := exec.Command("zenity",
		"--file-selection", "--title=Open map folder",
		"--filename=map.json",
		"--file-filter=Map manifest | map.json",
	).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
