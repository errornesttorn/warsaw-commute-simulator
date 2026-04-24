//go:build !darwin

package main

import (
	"fmt"
	"math"
	"os/exec"
	"strings"

	simpkg "github.com/errornesttorn/mini-traffic-simulation-core"
	rl "github.com/gen2brain/raylib-go/raylib"
)

const (
	screenW    = 1280
	screenH    = 720
	moveSpeed  = 20.0
	sprintMult = 3.0
	mouseSens  = 0.2
	groundY    = 0.0

	carHeight     = 1.5
	busHeight     = 3.2
	poleHeight    = 3.0
	lightHeadSize = 0.45
	pedHeight     = 1.75
	pedWidth      = 0.5
)

type App struct {
	world         *simpkg.World
	loaded        bool
	camPos        rl.Vector3
	yaw           float32
	pitch         float32
	mouseCaptured bool
	unitCube      rl.Model
}

func main() {
	rl.SetConfigFlags(rl.FlagWindowResizable | rl.FlagMsaa4xHint)
	rl.InitWindow(screenW, screenH, "Warsaw Commute - Driving Game")
	defer rl.CloseWindow()
	rl.SetTargetFPS(60)
	rl.DisableCursor()

	app := &App{
		camPos:        rl.NewVector3(0, 10, 0),
		pitch:         -20,
		mouseCaptured: true,
		unitCube:      rl.LoadModelFromMesh(rl.GenMeshCube(1, 1, 1)),
	}
	defer rl.UnloadModel(app.unitCube)

	for !rl.WindowShouldClose() {
		app.update()
		app.draw()
	}
}

// ---------- update ----------

func (a *App) update() {
	dt := rl.GetFrameTime()

	if rl.IsKeyPressed(rl.KeyEscape) {
		a.mouseCaptured = !a.mouseCaptured
		if a.mouseCaptured {
			rl.DisableCursor()
		} else {
			rl.EnableCursor()
		}
	}

	if (rl.IsKeyDown(rl.KeyLeftControl) || rl.IsKeyDown(rl.KeyRightControl)) && rl.IsKeyPressed(rl.KeyO) {
		a.openScenario()
	}

	if a.loaded {
		a.world.Step(dt)
	}

	if a.mouseCaptured {
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
	if rl.IsKeyDown(rl.KeyS) {
		a.camPos = addVec3(a.camPos, scaleVec3(fwd, -speed*dt))
	}
	if rl.IsKeyDown(rl.KeyA) {
		a.camPos = addVec3(a.camPos, scaleVec3(right, speed*dt))
	}
	if rl.IsKeyDown(rl.KeyD) {
		a.camPos = addVec3(a.camPos, scaleVec3(right, -speed*dt))
	}
	if rl.IsKeyDown(rl.KeySpace) {
		a.camPos.Y += speed * dt
	}
	if rl.IsKeyDown(rl.KeyLeftControl) && !rl.IsKeyDown(rl.KeyO) {
		a.camPos.Y -= speed * dt
	}
}

// ---------- draw ----------

func (a *App) draw() {
	rl.BeginDrawing()
	rl.ClearBackground(rl.NewColor(30, 30, 40, 255))

	rl.BeginMode3D(a.buildCamera())
	rl.DrawGrid(200, 1.0)

	if a.loaded {
		a.drawPedestrianPaths()
		a.drawSplines()
		a.drawTrafficLights()
		a.drawCars()
		a.drawPedestrians()
	}

	rl.EndMode3D()
	a.drawHUD()
	rl.EndDrawing()
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
		prev := sim2world(s.Samples[0])
		for i := 1; i < len(s.Samples); i++ {
			cur := sim2world(s.Samples[i])
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
		a.drawBox(rl.NewVector3(cx, groundY+0.02, cz), size, angle, col)
	}
}

func (a *App) drawTrafficLights() {
	for _, light := range a.world.TrafficLights {
		px := light.WorldPos.X
		pz := light.WorldPos.Y

		// Pole
		poleCenter := rl.NewVector3(px, groundY+poleHeight/2, pz)
		rl.DrawCubeV(poleCenter, rl.NewVector3(0.15, poleHeight, 0.15), rl.DarkGray)

		// Light head
		headY := float32(groundY + poleHeight + lightHeadSize/2)
		col := a.trafficLightColor(light)
		rl.DrawCubeV(rl.NewVector3(px, headY, pz), rl.NewVector3(lightHeadSize, lightHeadSize, lightHeadSize), col)
	}
}

func (a *App) drawCars() {
	allSplines := simpkg.MergedSplines(a.world.Splines, a.world.LaneChangeSplines)
	blinkOn := int(rl.GetTime()*2) % 2 == 0
	amber := rl.NewColor(255, 165, 0, 255)

	for _, car := range a.world.Cars {
		spline, ok := simpkg.FindSplineByID(allSplines, car.CurrentSplineID)
		if !ok {
			continue
		}
		pos2, heading2 := simpkg.SampleSplineAtDistance(spline, car.DistanceOnSpline)

		cx, cz := pos2.X, pos2.Y
		hx, hz := heading2.X, heading2.Y
		angle := float32(math.Atan2(float64(hx), float64(hz))) * 180 / math.Pi

		h := float32(carHeight)
		if car.VehicleKind == simpkg.VehicleBus {
			h = busHeight
		}
		length := car.Length
		width := car.Width

		c := car.Color
		a.drawBox(
			rl.NewVector3(cx, groundY+h/2, cz),
			rl.NewVector3(width, h, length),
			angle,
			rl.NewColor(c.R, c.G, c.B, 255),
		)

		// Trailer
		if car.Trailer.HasTrailer {
			rearX, rearZ := car.RearPosition.X, car.RearPosition.Y
			trX, trZ := car.Trailer.RearPosition.X, car.Trailer.RearPosition.Y
			tdx := rearX - trX
			tdz := rearZ - trZ
			trailerAngle := float32(math.Atan2(float64(tdx), float64(tdz))) * 180 / math.Pi
			trailerCenter := rl.NewVector3((rearX+trX)/2, groundY+h/2, (rearZ+trZ)/2)
			tc := car.Trailer.Color
			a.drawBox(
				trailerCenter,
				rl.NewVector3(car.Trailer.Width, h, car.Trailer.Length),
				trailerAngle,
				rl.NewColor(tc.R, tc.G, tc.B, 255),
			)
		}

		// Turn signal indicators (front corners)
		if blinkOn && car.TurnSignal != simpkg.TurnSignalNone {
			// right-of-car in 3D: perpendicular clockwise to heading
			// heading (hx,hz) → right = (hz, 0, -hx) in world
			rx, rz := hz, -hx
			halfLen := length/2 - 0.2
			halfWid := width/2 + 0.05
			indSize := rl.NewVector3(0.25, 0.25, 0.05)
			indY := groundY + h*0.7

			if car.TurnSignal == simpkg.TurnSignalLeft {
				// left = -right
				ix := cx + hx*halfLen + (-rx)*halfWid
				iz := cz + hz*halfLen + (-rz)*halfWid
				a.drawBox(rl.NewVector3(ix, indY, iz), indSize, angle, amber)
			} else {
				ix := cx + hx*halfLen + rx*halfWid
				iz := cz + hz*halfLen + rz*halfWid
				a.drawBox(rl.NewVector3(ix, indY, iz), indSize, angle, amber)
			}
		}
	}
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

		center := rl.NewVector3(px, groundY+pedHeight/2, pz)
		rl.DrawCubeV(center, rl.NewVector3(pedWidth, pedHeight, pedWidth*0.8), skinColor)
	}
}

// ---------- HUD ----------

func (a *App) drawHUD() {
	w := int32(rl.GetScreenWidth())
	h := int32(rl.GetScreenHeight())

	if !a.loaded {
		msg := "Press Ctrl+O to open a scenario"
		tw := rl.MeasureText(msg, 20)
		rl.DrawText(msg, w/2-tw/2, h/2-10, 20, rl.White)
	}

	cx, cy := w/2, h/2
	rl.DrawLine(cx-10, cy, cx+10, cy, rl.White)
	rl.DrawLine(cx, cy-10, cx, cy+10, rl.White)

	rl.DrawText("ESC: toggle mouse | Ctrl+O: open | WASD+Space/Ctrl: fly | Shift: sprint", 8, h-24, 14, rl.LightGray)

	if a.loaded {
		info := fmt.Sprintf("Cars: %d  Splines: %d  Peds: %d  Pos: (%.1f, %.1f, %.1f)",
			len(a.world.Cars), len(a.world.Splines), len(a.world.Pedestrians),
			a.camPos.X, a.camPos.Y, a.camPos.Z)
		rl.DrawText(info, 8, 8, 16, rl.White)
	}

	if !a.mouseCaptured {
		msg := "MOUSE RELEASED - press ESC to recapture"
		rl.DrawText(msg, w/2-int32(rl.MeasureText(msg, 16))/2, 8, 16, rl.Orange)
	}
}

// ---------- helpers ----------

func (a *App) drawBox(pos, size rl.Vector3, yawDeg float32, color rl.Color) {
	rl.DrawModelEx(a.unitCube, pos, rl.NewVector3(0, 1, 0), yawDeg, size, color)
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

func (a *App) openScenario() {
	rl.EnableCursor()
	path, err := pickFilePath()
	rl.DisableCursor()
	a.mouseCaptured = true
	if err != nil || path == "" {
		return
	}
	w, err := simpkg.LoadWorld(path)
	if err != nil {
		fmt.Printf("Failed to load scenario: %v\n", err)
		return
	}
	a.world = w
	a.loaded = true

	if len(w.Splines) > 0 {
		var sumX, sumZ float32
		var count float32
		for _, s := range w.Splines {
			sumX += s.P0.X + s.P3.X
			sumZ += s.P0.Y + s.P3.Y
			count += 2
		}
		a.camPos = rl.NewVector3(sumX/count, 50, sumZ/count)
		a.pitch = -45
	}
}

func sim2world(v simpkg.Vec2) rl.Vector3 {
	return rl.NewVector3(v.X, groundY, v.Y)
}

func addVec3(a, b rl.Vector3) rl.Vector3 {
	return rl.NewVector3(a.X+b.X, a.Y+b.Y, a.Z+b.Z)
}

func scaleVec3(v rl.Vector3, s float32) rl.Vector3 {
	return rl.NewVector3(v.X*s, v.Y*s, v.Z*s)
}

func pickFilePath() (string, error) {
	out, err := exec.Command("zenity",
		"--file-selection", "--title=Open scenario",
		"--file-filter=Scenario files | *.json",
	).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
