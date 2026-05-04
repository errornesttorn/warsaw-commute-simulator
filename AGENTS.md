# AGENTS.md

Guidance for coding agents working in `driving-game/`.

This file supplements the workspace-level `../AGENTS.md`. Follow both files;
the instructions here are more specific for this Go module.

## Project Overview

`driving-game` is a Linux-focused Go/Raylib 3D viewer and editor for Warsaw
commute data. It loads a terrain/map manifest, builds a textured terrain from
DEM and orthophoto tiles, streams building GLB regions near the camera, draws
generated tree/shrub foliage, renders editable road surfaces, loads editable
prop/object layers, and optionally overlays/runs a traffic simulation world
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
- `the-map/road_masks/*.json` - editable road mask polygons/curbs.
- `the-map/props/*.json` - editable prop and linear prop layers.
- `the-map/assets/props/**/*.glb` - GLB assets used by prop layers/editors.

Do not rewrite, delete, move, or bulk-format map assets unless the task
explicitly asks for fixture/data changes. Avoid broad commands that scan or
copy all assets unless needed.

## Build, Run, Test

Prerequisites:

- Go 1.22.2 or compatible Go 1.22.x.
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

Current test coverage includes focused unit tests for road-mask/road-surface
geometry, terrain base-height lookup, map road-mask path resolution, and camera
frustum culling. Add similarly focused tests for pure parsing, math, cache-key,
or data conversion behavior. Rendering, GPU upload, and interactive editor
behavior usually need manual runtime checks.

Always run `gofmt` on changed Go files. Run `go test ./...` after code
changes. For simulation contract changes, also test the sibling core module.

## Runtime Workflow

Run with `go run .`, then press `Ctrl+O` and select a `map.json` file or a map
folder containing one, such as `the-map/`.

Base controls:

- `WASD` - fly horizontally.
- `E` / `Q` - fly up/down.
- `Shift` - sprint; when spectating a car, exit spectate mode.
- `Tab` - release/recapture mouse in normal and road-mask modes.
- `Ctrl+O` - open map.
- `Space` - pause simulation.
- `P` - toggle path/spline overlay.
- `LMB` on a car - spectate that car.
- `F3` - toggle VRAM estimate overlay.
- `F11` - toggle fullscreen.
- `Esc` - quit, except road-mask mode uses `1` to exit.

Mode keys:

- `1` - normal/no editor mode.
- `2` - prop editor.
- `3` - road-mask editor.
- `4` - terrain geometry editor.

Prop editor controls:

- `B` - place single props.
- `N` - select/move props or linear props.
- `M` - draw linear prop runs.
- Click the asset list in the HUD to switch the active GLB asset.
- `LMB` - place/select/drag/add linear points depending on mode.
- `RMB` drag - rotate selected/current prop.
- `[` / `]` or `R` - rotate in key steps; hold `Shift` for smaller steps.
- `-` / `=` - scale selected/current prop.
- `,` / `.` - adjust linear spacing.
- `Enter` - commit a linear draft.
- `C` - clear a linear draft.
- `Delete` / `Backspace` - delete selected item or last draft point.
- `Ctrl+S` - save props to the primary prop layer JSON.

Road-mask editor controls:

- `G` - draw.
- `V` - edit nodes, spline handles, and segment properties.
- `R` - delete.
- `X` - cut/split an edge.
- `Z` or `L` - line segment mode.
- `C` - spline segment mode.
- `H` - hard curb.
- `F` - soft curb.
- `LMB` - draw/select/drag/delete/cut depending on tool.
- `Delete` / `Backspace` - straighten/remove selected segment content.
- `Esc` - cancel current selection or draft.
- `Ctrl+S` - save the road mask JSON and update `map.json` if needed.

Terrain geometry editor controls:

- `B` - raise/lower brush.
- `N` - level brush.
- `M` - smooth brush.
- `LMB` - apply current brush.
- `RMB` - lower terrain in raise/lower mode, or sample target height in level
  mode.
- Brush-size slider is shown in the HUD.
- `Ctrl+S` - write edited heights back to source DEM `.asc` tiles.

The app creates `.terrain-cache/` directories beside source assets. These
caches contain generated PNGs and DEM grid cache binaries and are safe to
regenerate. Do not hand-edit them.

## Source Map

- `main.go` - app loop, input, camera, HUD, simulation drawing, editor mode
  switching, map loading state machine, and file picker. It has
  `//go:build !darwin`; do not assume macOS support.
- `map_format.go` - `map.json` parsing, glob resolution, DEM metadata/grid
  processing, orthophoto mosaic construction, terrain grid cache, road/prop
  manifest path resolution, and `gdalinfo` bounds parsing.
- `terrain.go` - CPU terrain preparation, height normalization, orthophoto
  cache generation through `gdal_translate`, GPU terrain construction, road
  height overlay integration, and local/world height lookup.
- `terrain_tiles.go` - terrain tiling and async texture quality streaming.
- `scene_objects.go` - building GLB metadata/parser/upload structures, trees,
  shrub masks, generated foliage atlas/mesh, prop model drawing, and scene
  object drawing.
- `building_streaming.go` - building region streaming state machine, worker
  parsing, resident cap, distance-based eviction, quality upgrades, and view
  culling integration.
- `road_surfaces.go` - road mask JSON loading, road polygon/curb geometry,
  soft/hard curb height behavior, road mesh/cut generation, and road layer GPU
  upload/unload helpers.
- `road_mask_editor.go` - in-app road mask creation/editing, spline controls,
  JSON save/load, preview rebuilds, and manifest updates for new masks.
- `props.go` - prop/linear prop layer JSON parsing, asset discovery, GLB model
  loading, prop placement transforms, drawing, and save helpers.
- `prop_editor.go` - in-app prop and linear prop editor UI, picking, placement,
  transforms, deletion, and saving.
- `geometry_editor.go` - in-app DEM/terrain editor, terrain raycasting, brush
  operations, tile rebuild marking, and `.asc` saving.
- `view_culling.go` - camera frustum sphere visibility helper used to avoid
  drawing distant/off-screen scene objects.
- `vram_profiler.go` - approximate live VRAM accounting for terrain, roads,
  buildings, and foliage, plus frame-step timing display for rendering,
  streaming, and editor work.

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
- `road_mask`
- legacy `road_masks`, accepted only when it resolves to one file
- `prop_layers`
- legacy/equivalent `object_layers`

Relative paths are resolved from the manifest directory. Globs are supported
for the list fields and are sorted for deterministic loading. Empty entries
are ignored; unmatched globs are errors. `road_mask` must name one file, not a
glob.

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

Road mask files store nodes/edges in source raster pixel coordinates together
with a GeoTIFF transform. Editor code converts between local X/Z and pixel
coordinates when loading/saving. Closed edges become road height polygons; open
edges are ignored by road-surface generation.

Prop layer JSON stores point props and linear assets in world X/Y coordinates
with optional explicit Z. Missing Z means the prop follows terrain height.
Linear props expand along segment paths using `spacing_m`, scale, and heading
offset.

## Threading and GPU Rules

Raylib/GPU calls must stay on the main thread. Worker goroutines may parse
files, decode images, compute terrain/road data, and build CPU-side structures,
but must not call `rl.*` upload/draw/unload functions.

Important patterns to preserve:

- `openMap` starts CPU terrain/scene/road preparation in a goroutine.
- `advanceLoader` and `installLoadedMap` perform GPU terrain, road, prop, and
  foliage upload/loading on the main thread.
- `pumpTerrainStreaming` uploads terrain texture results incrementally per
  frame.
- `buildingStreamingWorker` parses GLBs in workers.
- `pumpBuildingStreaming` drains parsed results, uploads meshes/textures in
  bounded per-frame steps, handles upgrades, and evicts distant regions.
- `pumpGeometryTileRebuilds` rebuilds only one dirty terrain tile per frame.
- `pumpRoadMaskPreviewRebuild` throttles road preview rebuilds while dragging.
- `unloadTerrain` and `unloadSceneObjects` stop streaming workers before
  freeing GPU resources.

If you add new async work, keep the CPU/GPU split explicit and use small
per-frame upload budgets to avoid visible hitches.

## Performance Constraints

This app is dominated by texture, mesh, GLB, and geodata size. Treat these
constants as performance-sensitive:

- `terrainMeshMaxDim` and `terrainTextureMaxDim` in `main.go`.
- terrain tile grid/quality constants in `terrain_tiles.go`.
- building load/evict radii, resident cap, upload steps, and texture quality
  caps in `building_streaming.go`.
- road surface tessellation/cell/segment constants in `road_surfaces.go`.
- prop discovery/loading behavior in `props.go`.
- frustum visibility behavior in `view_culling.go`.

Before increasing dimensions, radii, resident counts, tessellation density, or
upload budgets, test with `the-map/` and inspect the `F3` VRAM overlay. Prefer
bounded streaming, culling, or cache changes over loading more data up front.

## Coding Guidelines

- Keep the package as `main`; follow the existing flat-file organization.
- Prefer small, local helpers over broad refactors.
- Preserve wrapped errors with context (`fmt.Errorf("...: %w", err)`).
- Keep map loading resilient: optional scene object, road, and prop failures
  should be reported and joined where appropriate, not necessarily make the
  whole terrain load fail.
- Do not introduce new heavy dependencies unless the task clearly needs them.
- Use structured parsers for JSON, CSV, GLB, ASC, and image work; avoid brittle
  string parsing for data formats.
- Keep comments for non-obvious Raylib, matrix, cgo, geodata transform, or
  streaming behavior.
- Avoid changing generated-looking binary or raster data as part of code-only
  tasks.
- Be especially cautious with editor save paths: prop, road-mask, and geometry
  editors intentionally write map data files.

## Verification Checklist

For most code changes:

```bash
gofmt -w <changed .go files>
go test ./...
```

For runtime/rendering/editor changes:

1. Run `go run .`.
2. Open `the-map/map.json`.
3. Confirm terrain appears, movement works, and no load errors are printed.
4. Move around enough to trigger terrain/building streaming and view culling.
5. Toggle `F3` and check that VRAM numbers look plausible.
6. Toggle `P` if simulation/path rendering changed.
7. Exercise the relevant editor mode: `2` props, `3` road masks, or `4`
   geometry. Only press `Ctrl+S` when the task intentionally updates map data.

If `gdalinfo`, `gdal_translate`, `zenity`, Raylib native libraries, or a GUI
display are unavailable, note that limitation in the final response.
