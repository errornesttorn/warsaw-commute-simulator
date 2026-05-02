package main

import (
	"fmt"

	rl "github.com/gen2brain/raylib-go/raylib"
)

// vramReport is an estimate; it walks the live GPU resources we know about
// and sums their byte sizes from CPU-side metadata. Driver overhead and
// shader/uniform buffers are not counted.
type vramReport struct {
	terrainTextures int64
	terrainMeshes   int64
	roadCutTex      int64
	roadMeshes      int64
	buildingTex     int64
	buildingMeshes  int64
	foliageTex      int64
	foliageMesh     int64

	terrainTileCount   int
	roadCutTexCount    int
	roadMeshCount      int
	buildingTexCount   int
	buildingMeshCount  int
	terrainTextureMaxW int32
	terrainTextureMinW int32

	buildingLowCount  int // regions at buildingQualityLow
	buildingMedCount  int // regions at buildingQualityMed
	buildingFullCount int // regions at buildingQualityFull
	buildingTexAvgW   int32
}

func textureBytes(tex rl.Texture2D) int64 {
	if tex.ID == 0 {
		return 0
	}
	base := int64(tex.Width) * int64(tex.Height) * textureBytesPerPixel(tex.Format)
	if tex.Mipmaps > 1 {
		// Geometric series 1 + 1/4 + 1/16 + ... ≈ 4/3 of base.
		return base * 4 / 3
	}
	return base
}

func textureBytesPerPixel(format rl.PixelFormat) int64 {
	switch format {
	case rl.UncompressedGrayscale:
		return 1
	case rl.UncompressedGrayAlpha, rl.UncompressedR5g6b5, rl.UncompressedR5g5b5a1, rl.UncompressedR4g4b4a4:
		return 2
	case rl.UncompressedR8g8b8:
		return 3
	case rl.UncompressedR32g32b32a32:
		return 16
	case rl.UncompressedR32g32b32:
		return 12
	case rl.UncompressedR32:
		return 4
	default:
		return 4
	}
}

func meshBytes(m rl.Mesh) int64 {
	vertexCount := int64(m.VertexCount)
	total := int64(0)
	if m.Vertices != nil {
		total += vertexCount * 3 * 4
	}
	if m.Texcoords != nil {
		total += vertexCount * 2 * 4
	}
	if m.Texcoords2 != nil {
		total += vertexCount * 2 * 4
	}
	if m.Normals != nil {
		total += vertexCount * 3 * 4
	}
	if m.Tangents != nil {
		total += vertexCount * 4 * 4
	}
	if m.Colors != nil {
		total += vertexCount * 4
	}
	if m.Indices != nil {
		total += int64(m.TriangleCount) * 3 * 2
	}
	return total
}

func collectVRAM(a *App) vramReport {
	var r vramReport
	if a.terrain != nil {
		r.terrainTileCount = len(a.terrain.tiles)
		r.terrainTextureMinW = 1 << 30
		for _, tile := range a.terrain.tiles {
			r.terrainTextures += textureBytes(tile.texture)
			if tile.meshBytes > 0 {
				r.terrainMeshes += tile.meshBytes
			} else {
				r.terrainMeshes += meshBytes(tile.mesh)
			}
			if tile.texture.Width > r.terrainTextureMaxW {
				r.terrainTextureMaxW = tile.texture.Width
			}
			if tile.texture.Width < r.terrainTextureMinW {
				r.terrainTextureMinW = tile.texture.Width
			}
			if tile.roadCut.ID != 0 {
				r.roadCutTex += textureBytes(tile.roadCut)
				r.roadCutTexCount++
			}
		}
		if r.terrainTileCount == 0 {
			r.terrainTextureMinW = 0
		}
		if roads := a.terrain.roads; roads != nil {
			for _, surface := range roads.Surfaces {
				if surface.Loaded {
					r.roadMeshes += surface.MeshBytes
					r.roadMeshCount++
				}
			}
			if roads.HardCurbs.Loaded {
				r.roadMeshes += roads.HardCurbs.MeshBytes
				r.roadMeshCount++
			}
		}
	}
	if a.objects != nil {
		var buildingTexWidthSum int64
		for _, region := range a.objects.BuildingRegions {
			switch region.Quality {
			case buildingQualityLow:
				r.buildingLowCount++
			case buildingQualityMed:
				r.buildingMedCount++
			case buildingQualityFull:
				r.buildingFullCount++
			}
			for _, tex := range region.Model.Textures {
				r.buildingTex += textureBytes(tex)
				r.buildingTexCount++
				buildingTexWidthSum += int64(tex.Width)
			}
			for _, m := range region.Model.Meshes {
				r.buildingMeshes += meshBytes(m)
				r.buildingMeshCount++
			}
		}
		if r.buildingTexCount > 0 {
			r.buildingTexAvgW = int32(buildingTexWidthSum / int64(r.buildingTexCount))
		}
		if a.objects.TreeFoliage.Loaded {
			r.foliageTex += textureBytes(a.objects.TreeFoliage.Texture)
			r.foliageMesh += meshBytes(a.objects.TreeFoliage.Mesh)
		}
	}
	return r
}

func formatBytes(b int64) string {
	const (
		kib = 1 << 10
		mib = 1 << 20
		gib = 1 << 30
	)
	switch {
	case b >= gib:
		return fmt.Sprintf("%.2f GiB", float64(b)/float64(gib))
	case b >= mib:
		return fmt.Sprintf("%.1f MiB", float64(b)/float64(mib))
	case b >= kib:
		return fmt.Sprintf("%.1f KiB", float64(b)/float64(kib))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func drawVRAMProfiler(a *App) {
	r := collectVRAM(a)

	terrainTotal := r.terrainTextures + r.terrainMeshes + r.roadCutTex + r.roadMeshes
	buildingTotal := r.buildingTex + r.buildingMeshes
	foliageTotal := r.foliageTex + r.foliageMesh
	grand := terrainTotal + buildingTotal + foliageTotal

	// Per-tile quality histogram.
	qCounts := [4]int{}
	if a.terrain != nil {
		for _, tile := range a.terrain.tiles {
			if tile.quality >= 0 && tile.quality < len(qCounts) {
				qCounts[tile.quality]++
			}
		}
	}

	lines := []string{
		"VRAM profiler  (F3 to toggle)",
		fmt.Sprintf("Total estimate:           %s", formatBytes(grand)),
		"",
		fmt.Sprintf("Terrain tiles (%d):       %s", r.terrainTileCount, formatBytes(terrainTotal)),
		fmt.Sprintf("  textures:               %s", formatBytes(r.terrainTextures)),
		fmt.Sprintf("  meshes:                 %s", formatBytes(r.terrainMeshes)),
		fmt.Sprintf("  road cuts (%d):          %s", r.roadCutTexCount, formatBytes(r.roadCutTex)),
		fmt.Sprintf("  road meshes (%d):        %s", r.roadMeshCount, formatBytes(r.roadMeshes)),
		fmt.Sprintf("  tile dim min/max:       %d / %d", r.terrainTextureMinW, r.terrainTextureMaxW),
		fmt.Sprintf("  quality base/high/ultra/extreme: %d / %d / %d / %d",
			qCounts[0], qCounts[1], qCounts[2], qCounts[3]),
		"",
		fmt.Sprintf("Buildings:                %s", formatBytes(buildingTotal)),
		fmt.Sprintf("  textures (%d):           %s", r.buildingTexCount, formatBytes(r.buildingTex)),
		fmt.Sprintf("  avg tex width:          %d px", r.buildingTexAvgW),
		fmt.Sprintf("  quality low/med/full:   %d / %d / %d", r.buildingLowCount, r.buildingMedCount, r.buildingFullCount),
		fmt.Sprintf("  meshes (%d):             %s", r.buildingMeshCount, formatBytes(r.buildingMeshes)),
		"",
		fmt.Sprintf("Foliage:                  %s", formatBytes(foliageTotal)),
		fmt.Sprintf("  texture:                %s", formatBytes(r.foliageTex)),
		fmt.Sprintf("  mesh:                   %s", formatBytes(r.foliageMesh)),
	}

	const (
		fontSize = 14
		lineH    = 18
		padding  = 8
	)
	maxW := int32(0)
	for _, l := range lines {
		w := rl.MeasureText(l, fontSize)
		if w > maxW {
			maxW = w
		}
	}
	boxW := maxW + 2*padding
	boxH := int32(len(lines))*lineH + 2*padding
	x := int32(rl.GetScreenWidth()) - boxW - 8
	y := int32(40)

	rl.DrawRectangle(x, y, boxW, boxH, rl.NewColor(0, 0, 0, 180))
	rl.DrawRectangleLines(x, y, boxW, boxH, rl.NewColor(120, 200, 255, 255))
	for i, l := range lines {
		rl.DrawText(l, x+padding, y+padding+int32(i)*lineH, fontSize, rl.White)
	}
}
