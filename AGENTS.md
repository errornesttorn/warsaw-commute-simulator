# AGENTS.md

Guidance for coding agents working in `driving-game/`.

This file supplements the workspace-level `../AGENTS.md`. Follow both files;
the instructions here are more specific for this Go module.

## Project Overview

`driving-game` is a Linux-focused Go/Raylib 3D viewer for Warsaw commute data.
It loads a terrain/map manifest, builds a textured terrain from DEM and
orthophoto tiles, streams building GLB regions near the camera, draws generated
tree/shrub foliage, and optionally overlays/runs a traffic simulation world
from `mini-traffic-simulation-core`.

The module is intentionally asset-heavy. The local `the-map/` directory is
several GiB and contains the sample runtime data used by the app:

- `the-map/map.json` - map manifest.
- `the-map/simulation.json` - simulation loaded through the core library.
- `the-map/dems/*.asc` - DEM height tiles.
- `the-map/subregions/*.tif` - orthophoto GeoTIFFs.
- `the-map/glb_regions_highres/*.glb` - building regions.
- `the-map/trees/*.csv` - tree instances.
- `the-map/shrubs/*.tif` - shrub masks.

Do not rewrite, delete, move, or bulk-format map assets unless the task
explicitly asks for fixture/data changes. Avoid broad commands that scan or
copy all assets unless needed.

## Build, Run, Test

Prerequisites:

- Go 1.22.x.
- cgo enabled and native Raylib/raylib-go dependencies installed.
- `gdalinfo` and `gdal_translate` on `PATH` for GeoTIFF bounds, orthophoto
  downsampling, and shrub mask conversion.
- `zenity` on `PATH` for the `Ctrl+O` map picker.
- The sibling module `../mini-traffic-simulation-core` must exist because
  `go.mod` has:
  `replace github.com/errornesttorn/mini-traffic-simulation-core => ../mini-traffic-simulation-core`.

Common commands from `driving-game/`:

```bash
go test ./...
go build .
go run .
```

Current test state: `go test ./...` passes and reports no test files. Add
focused tests for pure parsing/math/cache-key behavior when changing those
areas; rendering and GPU upload behavior usually needs manual runtime checks.

Always run `gofmt` on changed Go files. Run `go test ./...` after code
changes. For simulation contract changes, also test the sibling core module.

## Runtime Workflow

Run with `go run .`, then press `Ctrl+O` and select a `map.json` file or a map
folder containing one, such as `the-map/`.

Controls:

- `WASD` - fly horizontally.
- `E` / `Q` - fly up/down.
- `Shift` - sprint.
- `Tab` - release/recapture mouse.
- `Ctrl+O` - open map.
- `Space` - pause simulation.
- `P` - toggle path/spline overlay.
- `F3` - toggle VRAM estimate overlay.
- `Esc` - quit.

The app creates `.terrain-cache/` directories beside source assets. These
caches contain generated PNGs and DEM grid cache binaries and are safe to
regenerate. Do not hand-edit them.

## Source Map

- `main.go` - app loop, input, camera, HUD, simulation drawing, map loading
  state machine, and file picker. It has `//go:build !darwin`; do not assume
  macOS support.
- `map_format.go` - `map.json` parsing, glob resolution, DEM metadata/grid
  processing, orthophoto mosaic construction, terrain grid cache, and
  `gdalinfo` bounds parsing.
- `terrain.go` - CPU terrain preparation, height normalization, orthophoto
  cache generation through `gdal_translate`, GPU terrain construction, and
  local/world height lookup.
- `terrain_tiles.go` - terrain tiling and async texture quality streaming.
- `scene_objects.go` - building GLB metadata/parser/upload structures, trees,
  shrub masks, generated foliage atlas/mesh, and scene object drawing.
- `building_streaming.go` - building region streaming state machine, worker
  parsing, resident cap, distance-based eviction, and quality upgrades.
- `vram_profiler.go` - approximate live VRAM accounting for terrain,
  buildings, and foliage.

## Map and Coordinate Notes

`map.json` supports version `1` and these fields:

- `name`
- `simulation`
- `raylib_center`
- legacy `tiles`
- `dems`
- `orthos`
- `building_glbs`
- `tree_files`
- `shrub_masks`

Relative paths are resolved from the manifest directory. Globs are supported and
are sorted for deterministic loading. Empty entries are ignored; unmatched
globs are errors.

The source geodata is in a projected meter coordinate system. The code treats
source coordinates as world X/Y and maps them into Raylib local X/Z:

- local X = `worldX - centerWorldX`
- local Z = `centerWorldY - worldY`
- local Y/elevation = `worldZ - centerWorldZ`

Be careful with this inversion when adding spatial logic. In simulation code,
`simpkg.Vec2{X, Y}` maps to Raylib `{X, Z}`.

Building GLBs are expected to carry `extras.origin_epsg2180` and optional stats.
Their horizontal bounds are derived from POSITION accessor min/max values for
streaming distance checks. Tree CSVs must start with `x,y,h_nmt,height`.

## Threading and GPU Rules

Raylib/GPU calls must stay on the main thread. Worker goroutines may parse
files, decode images, compute terrain data, and build CPU-side structures, but
must not call `rl.*` upload/draw/unload functions.

Important patterns to preserve:

- `openMap` starts CPU terrain/scene preparation in a goroutine.
- `advanceLoader` performs GPU terrain and foliage upload on the main thread.
- `pumpTerrainStreaming` uploads terrain texture results incrementally per
  frame.
- `buildingStreamingWorker` parses GLBs in workers.
- `pumpBuildingStreaming` drains parsed results, uploads meshes/textures in
  bounded per-frame steps, handles upgrades, and evicts distant regions.
- `unloadTerrain` and `unloadSceneObjects` stop streaming workers before
  freeing GPU resources.

If you add new async work, keep the CPU/GPU split explicit and use small
per-frame upload budgets to avoid visible hitches.

## Performance Constraints

This app is dominated by texture, mesh, and geodata size. Treat these constants
as performance-sensitive:

- `terrainMeshMaxDim` and `terrainTextureMaxDim` in `main.go`.
- terrain tile grid/quality constants in `terrain_tiles.go`.
- building load/evict radii, resident cap, upload steps, and texture quality
  caps in `building_streaming.go`.

Before increasing dimensions, radii, resident counts, or upload budgets, test
with `the-map/` and inspect the `F3` VRAM overlay. Prefer bounded streaming or
cache changes over loading more data up front.

## Coding Guidelines

- Keep the package as `main`; follow the existing flat-file organization.
- Prefer small, local helpers over broad refactors.
- Preserve wrapped errors with context (`fmt.Errorf("...: %w", err)`).
- Keep map loading resilient: optional scene object failures should be reported
  and joined where appropriate, not necessarily make the whole terrain load
  fail.
- Do not introduce new heavy dependencies unless the task clearly needs them.
- Use structured parsers for JSON, CSV, GLB, and image work; avoid brittle
  string parsing for data formats.
- Keep comments for non-obvious Raylib, matrix, cgo, or streaming behavior.
- Avoid changing generated-looking binary or raster data as part of code-only
  tasks.

## Verification Checklist

For most code changes:

```bash
gofmt -w <changed .go files>
go test ./...
```

For runtime/rendering changes:

1. Run `go run .`.
2. Open `the-map/map.json`.
3. Confirm terrain appears, movement works, and no load errors are printed.
4. Move around enough to trigger terrain/building streaming.
5. Toggle `F3` and check that VRAM numbers look plausible.
6. Toggle `P` if simulation/path rendering changed.

If `gdalinfo`, `gdal_translate`, `zenity`, Raylib native libraries, or a GUI
display are unavailable, note that limitation in the final response.
