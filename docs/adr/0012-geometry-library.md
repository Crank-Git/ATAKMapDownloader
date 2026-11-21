# 0012 — orb holds the area geometry, and tilecalc selects the tiles

Date: 2026-11-21
Status: accepted

## Context

Feature 11 adds `polygon` and `route_buffer` areas (FR-area-selection-1 to 3). `tilecalc` must select every tile whose square intersects a polygon, or is within `buffer_m` of a line (FR-area-selection-6 and 7). Later issues decode GeoJSON and KML into the same geometry, so the type must suit a decoder too.

The options were:

- `github.com/paulmach/orb` v0.14.0, released 2026-11-01. It has the GeoJSON geometry types, a GeoJSON decoder in `orb/geojson`, and a tile cover in `orb/maptile/tilecover`.
- Geometry types and the intersection code written by hand in `tilecalc`.

The `LICENSE.md` file of orb v0.14.0 is the MIT License, "Copyright (c) 2017 Paul Mach". The project is MIT, so the license is compatible. Source: https://github.com/paulmach/orb, tag v0.14.0, retrieved 2026-11-21.

The root `orb` package imports only the standard library. `orb/geojson` imports `go.mongodb.org/mongo-driver/v2/bson` for its BSON methods. `tilecover.Polygon` returns a `maptile.Set`, which holds every tile of one zoom level in memory. orb has no buffer around a line.

## Decision

`tilecalc.Area` holds its geometry as an `orb.Geometry`. `tilecalc` imports only the root `orb` package. The tile selection for polygons and route buffers is written in `internal/tilecalc/area.go`:

- A polygon tile is a tile whose square meets an edge, or whose center is inside the polygon.
- A route buffer tile is a tile whose square is within the buffer of one line segment.
- Both work in Web Mercator tile units at each zoom level, and send tiles one row at a time.

## Consequences

- A GeoJSON decoder can produce an `Area` with no type conversion.
- `tilecalc` never holds a full tile set in memory, so a large area at a high zoom level costs time, not memory.
- `orb/geojson` adds the mongo-driver `bson` package to the binary. An issue that imports it must accept that size, or decode GeoJSON into `orb` types itself.
- The self-intersection check compares every pair of edges in a polygon. A polygon with many thousands of positions takes longer to validate. A sweep-line check can replace it if that becomes a problem.
- An orb upgrade must keep the root package free of new dependencies, or this decision needs a review.
