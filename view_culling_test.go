package main

import (
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
