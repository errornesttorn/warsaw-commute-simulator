package main

import (
	"math"
	"testing"

	simpkg "github.com/errornesttorn/mini-traffic-simulation-core"
)

func TestPedestrianBodyPoseBackwardUsesP1Origin(t *testing.T) {
	paths := []simpkg.PedestrianPath{{
		P0: simpkg.NewVec2(0, 0),
		P1: simpkg.NewVec2(10, 0),
	}}
	ped := simpkg.Pedestrian{
		PathIndex:        0,
		Distance:         2,
		Forward:          false,
		LateralOffset:    -0.75,
		TransitionActive: false,
	}

	pos, heading, ok := pedestrianBodyPose(paths, ped)
	if !ok {
		t.Fatal("pedestrian pose should be valid")
	}
	assertVec2Near(t, pos, simpkg.NewVec2(8, -0.75))
	assertVec2Near(t, heading, simpkg.NewVec2(-1, 0))
}

func TestPedestrianBodyPoseTransitionUsesBezier(t *testing.T) {
	paths := []simpkg.PedestrianPath{{
		P0: simpkg.NewVec2(0, 0),
		P1: simpkg.NewVec2(10, 0),
	}}
	ped := simpkg.Pedestrian{
		PathIndex:           0,
		Forward:             true,
		TransitionActive:    true,
		TransitionDistance:  5,
		TransitionLength:    10,
		TransitionP0:        simpkg.NewVec2(0, 0),
		TransitionP1:        simpkg.NewVec2(10, 0),
		TransitionP2:        simpkg.NewVec2(10, 10),
		TransitionNextPath:  0,
		TransitionEndOffset: 0,
	}

	pos, heading, ok := pedestrianBodyPose(paths, ped)
	if !ok {
		t.Fatal("pedestrian pose should be valid")
	}
	assertVec2Near(t, pos, simpkg.NewVec2(7.5, 2.5))
	assertVec2Near(t, heading, simpkg.NewVec2(float32(math.Sqrt(0.5)), float32(math.Sqrt(0.5))))
}

func assertVec2Near(t *testing.T, got, want simpkg.Vec2) {
	t.Helper()
	const epsilon = 1e-5
	if math.Abs(float64(got.X-want.X)) > epsilon || math.Abs(float64(got.Y-want.Y)) > epsilon {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
