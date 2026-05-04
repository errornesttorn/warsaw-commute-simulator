package main

import (
	"math"
	"testing"

	rl "github.com/gen2brain/raylib-go/raylib"
)

func TestCameraVisibilitySphereVisible(t *testing.T) {
	camera := rl.Camera{
		Position: rl.NewVector3(0, 0, 0),
		Target:   rl.NewVector3(0, 0, 1),
		Up:       rl.NewVector3(0, 1, 0),
		Fovy:     90,
	}
	view := newCameraVisibility(camera, 1, 100)

	if !view.sphereVisible(rl.NewVector3(0, 0, 10), 1) {
		t.Fatal("sphere in front of camera should be visible")
	}
	if view.sphereVisible(rl.NewVector3(0, 0, -10), 1) {
		t.Fatal("sphere behind camera should be culled")
	}
	if view.sphereVisible(rl.NewVector3(50, 0, 10), 1) {
		t.Fatal("sphere outside horizontal view cone should be culled")
	}
}

func TestCameraVisibilitySphereEdgeUsesFrustumPlaneDistance(t *testing.T) {
	camera := rl.Camera{
		Position: rl.NewVector3(0, 0, 0),
		Target:   rl.NewVector3(0, 0, 1),
		Up:       rl.NewVector3(0, 1, 0),
		Fovy:     70,
	}
	view := newCameraVisibility(camera, 16.0/9.0, 100)

	depth := float32(10)
	radius := float32(1)
	edgeX := depth * view.tanX
	planeMargin := radius * float32(math.Sqrt(float64(1+view.tanX*view.tanX)))

	if !view.sphereVisible(rl.NewVector3(edgeX+planeMargin*0.75, 0, depth), radius) {
		t.Fatal("sphere intersecting horizontal frustum edge should remain visible")
	}
	if view.sphereVisible(rl.NewVector3(edgeX+planeMargin*1.25, 0, depth), radius) {
		t.Fatal("sphere fully outside horizontal frustum edge should be culled")
	}
}
