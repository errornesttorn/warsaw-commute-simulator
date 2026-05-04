package main

import (
	"math"

	rl "github.com/gen2brain/raylib-go/raylib"
)

type cameraVisibility struct {
	position rl.Vector3
	forward  rl.Vector3
	right    rl.Vector3
	up       rl.Vector3
	tanX     float32
	tanY     float32
	far      float32
}

func newCameraVisibility(camera rl.Camera, aspect, far float32) cameraVisibility {
	forward := normalizeVec3(rl.NewVector3(
		camera.Target.X-camera.Position.X,
		camera.Target.Y-camera.Position.Y,
		camera.Target.Z-camera.Position.Z,
	))
	if vec3LengthSquared(forward) <= 1e-8 {
		forward = rl.NewVector3(0, 0, 1)
	}
	right := normalizeVec3(crossVec3(forward, camera.Up))
	if vec3LengthSquared(right) <= 1e-8 {
		right = rl.NewVector3(1, 0, 0)
	}
	up := normalizeVec3(crossVec3(right, forward))
	if aspect <= 0 {
		aspect = 1
	}
	tanY := float32(math.Tan(float64(camera.Fovy) * math.Pi / 360))
	return cameraVisibility{
		position: camera.Position,
		forward:  forward,
		right:    right,
		up:       up,
		tanX:     tanY * aspect,
		tanY:     tanY,
		far:      far,
	}
}

func (v cameraVisibility) sphereVisible(center rl.Vector3, radius float32) bool {
	toCenter := rl.NewVector3(
		center.X-v.position.X,
		center.Y-v.position.Y,
		center.Z-v.position.Z,
	)
	depth := dotVec3(toCenter, v.forward)
	if depth < -radius || depth > v.far+radius {
		return false
	}
	x := float32(math.Abs(float64(dotVec3(toCenter, v.right))))
	if x > max32(depth, 0)*v.tanX+radius*float32(math.Sqrt(float64(1+v.tanX*v.tanX))) {
		return false
	}
	y := float32(math.Abs(float64(dotVec3(toCenter, v.up))))
	return y <= max32(depth, 0)*v.tanY+radius*float32(math.Sqrt(float64(1+v.tanY*v.tanY)))
}

func vec3LengthSquared(v rl.Vector3) float32 {
	return v.X*v.X + v.Y*v.Y + v.Z*v.Z
}

func normalizeVec3(v rl.Vector3) rl.Vector3 {
	l2 := vec3LengthSquared(v)
	if l2 <= 1e-12 {
		return rl.Vector3{}
	}
	inv := 1 / float32(math.Sqrt(float64(l2)))
	return rl.NewVector3(v.X*inv, v.Y*inv, v.Z*inv)
}

func dotVec3(a, b rl.Vector3) float32 {
	return a.X*b.X + a.Y*b.Y + a.Z*b.Z
}

func crossVec3(a, b rl.Vector3) rl.Vector3 {
	return rl.NewVector3(
		a.Y*b.Z-a.Z*b.Y,
		a.Z*b.X-a.X*b.Z,
		a.X*b.Y-a.Y*b.X,
	)
}
