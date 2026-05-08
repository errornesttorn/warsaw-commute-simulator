package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	simpkg "github.com/errornesttorn/mini-traffic-simulation-core"
	rl "github.com/gen2brain/raylib-go/raylib"
)

const (
	vehicleAssetsRoot      = "assets/vehicles"
	vehicleBodyFile        = "body.glb"
	vehicleWheelFile       = "wheel.glb"
	minVehicleWheelRadiusM = float32(0.05)

	vehicleModelFullDistanceM = float32(150)
	vehicleModelBodyDistanceM = float32(500)
)

type vehicleModelLOD int

const (
	vehicleModelLODNone vehicleModelLOD = iota
	vehicleModelLODBody
	vehicleModelLODFull
)

type vehicleAsset struct {
	ModelID         string
	Dir             string
	Body            rl.Model
	Wheel           rl.Model
	BodyBounds      rl.BoundingBox
	WheelBounds     rl.BoundingBox
	BodyCorrection  rl.Matrix
	WheelCorrection rl.Matrix
	WheelRadius     float32
	WheelThickness  float32
	Loaded          bool
	Err             error
	FitErrReported  bool
}

func (a *App) drawVehicleModel(car simpkg.Car, center, bodyHeading simpkg.Vec2, ground, yawDeg, pitchDeg, rollDeg float32, lod vehicleModelLOD) bool {
	if car.Length <= 0 || car.Width <= 0 {
		return false
	}
	asset := a.ensureVehicleAsset(car.ModelID)
	if asset == nil || !asset.Loaded {
		return false
	}
	if err := vehicleAssetFitError(asset, car); err != nil {
		if !asset.FitErrReported {
			fmt.Printf("Vehicle model %q does not match renderer axis conventions: %v; using cuboid fallback\n", car.ModelID, err)
			asset.FitErrReported = true
		}
		return false
	}

	scale := vehicleAssetScale(asset, car)
	bodyPos := rl.NewVector3(center.X, ground, center.Y)
	bodyFrame := vehicleFrameTransform(bodyPos, yawDeg, pitchDeg, rollDeg)
	bodyTransform := matrixChain(
		asset.BodyCorrection,
		rl.MatrixScale(scale, scale, scale),
		bodyFrame,
	)
	drawModelWithTransform(asset.Body, bodyTransform)
	if lod != vehicleModelLODFull {
		return true
	}

	wheelRadius := asset.WheelRadius * scale
	if wheelRadius < minVehicleWheelRadiusM {
		wheelRadius = minVehicleWheelRadiusM
	}
	wheelThickness := asset.WheelThickness * scale
	if wheelThickness < 0 {
		wheelThickness = 0
	}

	trackX := car.Width*0.5 - wheelThickness*0.5
	if trackX <= 0 {
		trackX = car.Width * 0.5
	}
	wheelY := wheelRadius
	frontZ := vehiclePivotLocalZ(car.Length, vehicleFrontPivotFrac(car))
	rearZ := vehiclePivotLocalZ(car.Length, vehicleRearPivotFrac(car))

	spinDeg := a.advanceVehicleWheelSpin(car, wheelRadius)
	steerDeg := vehicleSteerAngleDeg(bodyHeading, car.Heading)

	a.drawVehicleWheel(asset, bodyFrame, scale, -trackX, wheelY, frontZ, steerDeg, spinDeg, -1)
	a.drawVehicleWheel(asset, bodyFrame, scale, trackX, wheelY, frontZ, steerDeg, spinDeg, 1)
	a.drawVehicleWheel(asset, bodyFrame, scale, -trackX, wheelY, rearZ, 0, spinDeg, -1)
	a.drawVehicleWheel(asset, bodyFrame, scale, trackX, wheelY, rearZ, 0, spinDeg, 1)
	return true
}

func (a *App) vehicleModelLOD(car simpkg.Car, center rl.Vector3) vehicleModelLOD {
	if strings.TrimSpace(car.ModelID) == "" {
		return vehicleModelLODNone
	}
	if !a.vehicleWithinDrawDistance(center) {
		return vehicleModelLODNone
	}
	dx := center.X - a.camPos.X
	dy := center.Y - a.camPos.Y
	dz := center.Z - a.camPos.Z
	distSq := dx*dx + dy*dy + dz*dz
	if car.ID == a.spectatedCarID || distSq <= vehicleModelFullDistanceM*vehicleModelFullDistanceM {
		return vehicleModelLODFull
	}
	if distSq <= vehicleModelBodyDistanceM*vehicleModelBodyDistanceM {
		return vehicleModelLODBody
	}
	return vehicleModelLODNone
}

func (a *App) vehicleWithinDrawDistance(center rl.Vector3) bool {
	dx := center.X - a.camPos.X
	dy := center.Y - a.camPos.Y
	dz := center.Z - a.camPos.Z
	return dx*dx+dy*dy+dz*dz <= vehicleModelBodyDistanceM*vehicleModelBodyDistanceM
}

func (a *App) drawVehicleWheel(asset *vehicleAsset, bodyFrame rl.Matrix, scale, localX, localY, localZ, steerDeg, spinDeg, side float32) {
	if asset == nil || !asset.Loaded {
		return
	}
	if side < 0 {
		// The wheel convention is local +X = outward face. Left-side wheels
		// need a half-turn around local Y so the detailed face points outward.
		spinDeg = -spinDeg
	}
	transform := matrixChain(
		asset.WheelCorrection,
		rl.MatrixScale(scale, scale, scale),
		rl.MatrixRotate(rl.NewVector3(1, 0, 0), degToRad(spinDeg)),
		vehicleWheelSideTransform(side),
		rl.MatrixRotate(rl.NewVector3(0, 1, 0), degToRad(steerDeg)),
		rl.MatrixTranslate(localX, localY, localZ),
		bodyFrame,
	)
	drawModelWithTransform(asset.Wheel, transform)
}

func (a *App) ensureVehicleAsset(modelID string) *vehicleAsset {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil
	}
	if a.vehicleAssets == nil {
		a.vehicleAssets = map[string]*vehicleAsset{}
	}
	if asset, ok := a.vehicleAssets[modelID]; ok {
		if asset.Loaded {
			return asset
		}
		return nil
	}

	asset, err := loadVehicleAsset(modelID)
	if err != nil {
		a.vehicleAssets[modelID] = &vehicleAsset{ModelID: modelID, Err: err}
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Printf("Vehicle model %q unavailable: %v\n", modelID, err)
		}
		return nil
	}
	a.vehicleAssets[modelID] = asset
	return asset
}

func loadVehicleAsset(modelID string) (*vehicleAsset, error) {
	dir, ok := vehicleAssetDir(modelID)
	if !ok {
		return nil, fmt.Errorf("invalid vehicle model id %q", modelID)
	}
	bodyPath := filepath.Join(dir, vehicleBodyFile)
	wheelPath := filepath.Join(dir, vehicleWheelFile)
	if _, err := os.Stat(bodyPath); err != nil {
		return nil, err
	}
	if _, err := os.Stat(wheelPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s: missing %s", modelID, vehicleWheelFile)
		}
		return nil, err
	}

	body := rl.LoadModel(bodyPath)
	if !rl.IsModelValid(body) {
		return nil, fmt.Errorf("%s: load %s: invalid model", modelID, vehicleBodyFile)
	}
	applyModelLightingToModel(&body)
	wheel := rl.LoadModel(wheelPath)
	if !rl.IsModelValid(wheel) {
		rl.UnloadModel(body)
		return nil, fmt.Errorf("%s: load %s: invalid model", modelID, vehicleWheelFile)
	}
	applyModelLightingToModel(&wheel)

	bodyBounds := rl.GetModelBoundingBox(body)
	wheelBounds := rl.GetModelBoundingBox(wheel)
	wheelRadius, wheelThickness := vehicleWheelMetrics(wheelBounds)
	if wheelRadius < minVehicleWheelRadiusM {
		rl.UnloadModel(body)
		rl.UnloadModel(wheel)
		return nil, fmt.Errorf("%s: %s radius %.3fm is too small", modelID, vehicleWheelFile, wheelRadius)
	}

	return &vehicleAsset{
		ModelID:         modelID,
		Dir:             dir,
		Body:            body,
		Wheel:           wheel,
		BodyBounds:      bodyBounds,
		WheelBounds:     wheelBounds,
		BodyCorrection:  vehicleBodyCorrection(bodyBounds),
		WheelCorrection: vehicleBoundsCenterCorrection(wheelBounds),
		WheelRadius:     wheelRadius,
		WheelThickness:  wheelThickness,
		Loaded:          true,
	}, nil
}

func unloadVehicleAssets(assets map[string]*vehicleAsset) {
	for _, asset := range assets {
		if asset == nil || !asset.Loaded {
			continue
		}
		rl.UnloadModel(asset.Body)
		rl.UnloadModel(asset.Wheel)
		asset.Loaded = false
	}
}

func vehicleAssetDir(modelID string) (string, bool) {
	if modelID == "" || modelID == "." || modelID == ".." {
		return "", false
	}
	if filepath.Base(modelID) != modelID || strings.ContainsAny(modelID, `/\`) {
		return "", false
	}
	return filepath.Join(vehicleAssetsRoot, modelID), true
}

func vehicleAssetScale(asset *vehicleAsset, car simpkg.Car) float32 {
	if asset == nil || car.Length <= 0 {
		return 1
	}
	bodyLength := asset.BodyBounds.Max.Z - asset.BodyBounds.Min.Z
	if bodyLength <= 0 {
		return 1
	}
	scale := car.Length / bodyLength
	if scale <= 0 || math.IsNaN(float64(scale)) || math.IsInf(float64(scale), 0) {
		return 1
	}
	return scale
}

func vehicleAssetFitError(asset *vehicleAsset, car simpkg.Car) error {
	if asset == nil || car.Length <= 0 || car.Width <= 0 {
		return errors.New("missing vehicle dimensions")
	}
	scale := vehicleAssetScale(asset, car)
	bodyWidth := (asset.BodyBounds.Max.X - asset.BodyBounds.Min.X) * scale
	if bodyWidth <= 0 {
		return errors.New("body has non-positive local X width")
	}
	widthRatio := bodyWidth / car.Width
	if widthRatio < 0.45 || widthRatio > 1.8 {
		return fmt.Errorf("body width after length scaling is %.2fm for simulation width %.2fm; expected local X=width and local Z=length", bodyWidth, car.Width)
	}
	if asset.WheelThickness > asset.WheelRadius*1.5 {
		return fmt.Errorf("wheel thickness %.2fm is too large for radius %.2fm; expected wheel axle/thickness on local X", asset.WheelThickness, asset.WheelRadius)
	}
	return nil
}

func vehicleWheelMetrics(bounds rl.BoundingBox) (radius, thickness float32) {
	sizeY := bounds.Max.Y - bounds.Min.Y
	sizeZ := bounds.Max.Z - bounds.Min.Z
	radius = max32(sizeY, sizeZ) * 0.5
	thickness = bounds.Max.X - bounds.Min.X
	return radius, thickness
}

func vehicleBodyCorrection(bounds rl.BoundingBox) rl.Matrix {
	center := boundsCenter(bounds)
	return rl.MatrixTranslate(-center.X, 0, -center.Z)
}

func vehicleBoundsCenterCorrection(bounds rl.BoundingBox) rl.Matrix {
	center := boundsCenter(bounds)
	return rl.MatrixTranslate(-center.X, -center.Y, -center.Z)
}

func boundsCenter(bounds rl.BoundingBox) rl.Vector3 {
	return rl.NewVector3(
		(bounds.Min.X+bounds.Max.X)*0.5,
		(bounds.Min.Y+bounds.Max.Y)*0.5,
		(bounds.Min.Z+bounds.Max.Z)*0.5,
	)
}

func vehicleBodyCenter(car simpkg.Car, frontPos, bodyHeading simpkg.Vec2) simpkg.Vec2 {
	frontLocalZ := vehiclePivotLocalZ(car.Length, vehicleFrontPivotFrac(car))
	return vec2Sub(frontPos, vec2Scale(bodyHeading, frontLocalZ))
}

func vehiclePivotLocalZ(length, frac float32) float32 {
	return length * (0.5 - frac)
}

func vehicleFrontPivotFrac(car simpkg.Car) float32 {
	if car.FrontPivotFrac > 0 && car.FrontPivotFrac < 1 {
		return car.FrontPivotFrac
	}
	return fallbackCarFrontPivotFrac
}

func vehicleRearPivotFrac(car simpkg.Car) float32 {
	if car.RearPivotFrac > 0 && car.RearPivotFrac < 1 && car.RearPivotFrac > vehicleFrontPivotFrac(car) {
		return car.RearPivotFrac
	}
	rear := vehicleFrontPivotFrac(car) + car.WheelbaseFrac()
	if rear > 0 && rear < 1 {
		return rear
	}
	return 1 - fallbackCarFrontPivotFrac
}

func vehicleSteerAngleDeg(bodyHeading, wheelHeading simpkg.Vec2) float32 {
	if vec2LengthSq(bodyHeading) <= 1e-9 || vec2LengthSq(wheelHeading) <= 1e-9 {
		return 0
	}
	bodyYaw := vehicleYawDeg(vec2Normalize(bodyHeading))
	wheelYaw := vehicleYawDeg(vec2Normalize(wheelHeading))
	return normalizeSignedDegrees(wheelYaw - bodyYaw)
}

func vehicleYawDeg(v simpkg.Vec2) float32 {
	return float32(math.Atan2(float64(v.X), float64(v.Y)) * 180 / math.Pi)
}

func normalizeSignedDegrees(v float32) float32 {
	for v <= -180 {
		v += 360
	}
	for v > 180 {
		v -= 360
	}
	return v
}

func (a *App) advanceVehicleWheelSpin(car simpkg.Car, wheelRadius float32) float32 {
	if wheelRadius < minVehicleWheelRadiusM {
		wheelRadius = minVehicleWheelRadiusM
	}
	if a.vehicleWheelSpin == nil {
		a.vehicleWheelSpin = map[int]float32{}
	}
	spin := a.vehicleWheelSpin[car.ID]
	if !a.paused {
		spin += car.Speed * rl.GetFrameTime() / wheelRadius
		spin = float32(math.Mod(float64(spin), math.Pi*2))
	}
	a.vehicleWheelSpin[car.ID] = spin
	return spin * 180 / math.Pi
}

func vehicleFrameTransform(pos rl.Vector3, yawDeg, pitchDeg, rollDeg float32) rl.Matrix {
	return boxTransform(pos, rl.NewVector3(1, 1, 1), yawDeg, pitchDeg, rollDeg)
}

func vehicleWheelSideTransform(side float32) rl.Matrix {
	if side < 0 {
		return rl.MatrixRotate(rl.NewVector3(0, 1, 0), float32(math.Pi))
	}
	return rl.MatrixIdentity()
}

func degToRad(deg float32) float32 {
	return deg * math.Pi / 180
}

func matrixChain(first rl.Matrix, rest ...rl.Matrix) rl.Matrix {
	m := first
	for _, next := range rest {
		m = rl.MatrixMultiply(m, next)
	}
	return m
}

func drawModelWithTransform(model rl.Model, transform rl.Matrix) {
	meshes := model.GetMeshes()
	materials := model.GetMaterials()
	if len(meshes) == 0 || len(materials) == 0 {
		return
	}
	meshMaterials := modelMeshMaterials(model)
	transform = rl.MatrixMultiply(model.Transform, transform)
	for i, mesh := range meshes {
		materialIndex := 0
		if i < len(meshMaterials) {
			materialIndex = int(meshMaterials[i])
		}
		if materialIndex < 0 || materialIndex >= len(materials) {
			materialIndex = 0
		}
		rl.DrawMesh(mesh, materials[materialIndex], transform)
	}
}

func modelMeshMaterials(model rl.Model) []int32 {
	if model.MeshMaterial == nil || model.MeshCount <= 0 {
		return nil
	}
	return unsafe.Slice(model.MeshMaterial, model.MeshCount)
}
