package main

import (
	"math"
	"testing"

	simpkg "github.com/errornesttorn/mini-traffic-simulation-core"
	rl "github.com/gen2brain/raylib-go/raylib"
)

func TestVehicleBodyCenterUsesFrontPivotFraction(t *testing.T) {
	car := simpkg.Car{
		Length:         4.70,
		FrontPivotFrac: 0.20,
	}
	front := simpkg.Vec2{X: 10, Y: 5}
	heading := simpkg.Vec2{X: 0, Y: 1}

	center := vehicleBodyCenter(car, front, heading)
	wantY := float32(5 - 4.70*(0.5-0.20))
	if abs32(center.X-10) > 1e-5 || abs32(center.Y-wantY) > 1e-5 {
		t.Fatalf("center = (%.4f, %.4f), want (10, %.4f)", center.X, center.Y, wantY)
	}
}

func TestVehiclePivotLocalZ(t *testing.T) {
	if got := vehiclePivotLocalZ(4, 0.25); abs32(got-1) > 1e-5 {
		t.Fatalf("front pivot local z = %.4f, want 1", got)
	}
	if got := vehiclePivotLocalZ(4, 0.75); abs32(got+1) > 1e-5 {
		t.Fatalf("rear pivot local z = %.4f, want -1", got)
	}
}

func TestVehicleSteerAngleDegUsesSimulationHeading(t *testing.T) {
	body := simpkg.Vec2{X: 0, Y: 1}
	right := simpkg.Vec2{X: 1, Y: 0}
	left := simpkg.Vec2{X: -1, Y: 0}

	if got := vehicleSteerAngleDeg(body, right); abs32(got-90) > 1e-5 {
		t.Fatalf("right steer = %.4f, want 90", got)
	}
	if got := vehicleSteerAngleDeg(body, left); abs32(got+90) > 1e-5 {
		t.Fatalf("left steer = %.4f, want -90", got)
	}
}

func TestVehicleModelLOD(t *testing.T) {
	app := &App{
		camPos:         rl.NewVector3(0, 0, 0),
		spectatedCarID: noSpectatedCarID,
	}
	car := simpkg.Car{ID: 10, ModelID: "test_car"}

	if got := app.vehicleModelLOD(car, rl.NewVector3(vehicleModelFullDistanceM-1, 0, 0)); got != vehicleModelLODFull {
		t.Fatalf("near LOD = %v, want full", got)
	}
	if got := app.vehicleModelLOD(car, rl.NewVector3(vehicleModelFullDistanceM+5, 0, 0)); got != vehicleModelLODBody {
		t.Fatalf("mid LOD = %v, want body", got)
	}
	if got := app.vehicleModelLOD(car, rl.NewVector3(vehicleModelBodyDistanceM+1, 0, 0)); got != vehicleModelLODNone {
		t.Fatalf("far LOD = %v, want none", got)
	}

	app.spectatedCarID = car.ID
	if got := app.vehicleModelLOD(car, rl.NewVector3(vehicleModelBodyDistanceM*4, 0, 0)); got != vehicleModelLODNone {
		t.Fatalf("far spectated LOD = %v, want none", got)
	}
}

func abs32(v float32) float32 {
	return float32(math.Abs(float64(v)))
}
