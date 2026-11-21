# 0010 — Leaflet-Geoman draws the area on the map

Date: 2026-11-21
Status: accepted

## Context

The web UI drew the rectangle with leaflet-draw 1.0.4 from unpkg.com. Leaflet.draw has had no release since 2018. Feature 08 vendors every asset, so the web UI loads with no internet (FR-offline-web-ui-1). Epic 7 adds polygon areas, so the drawing library must also draw polygons.

The options were:

- Leaflet-Geoman, free edition, v2.20.8. It draws rectangles and polygons, and it has releases in 2025 and 2026.
- Leaflet's own `L.Rectangle` with a small drag handler in `app.js`. It adds almost no bytes, but Epic 7 must then write polygon drawing.
- leaflet-draw 1.0.4, vendored. It has no maintenance.

Feature 08 requires an MIT-compatible license for Geoman, else the `L.Rectangle` fallback. The `LICENSE` file in the npm tarball `@geoman-io/leaflet-geoman-free@2.20.8` is the MIT License, "Copyright (c) 2017 Sumit Kumar". Its `package.json` also says `"license": "MIT"`. The project is MIT, so the license is compatible.

## Decision

The web UI uses Leaflet-Geoman free edition 2.20.8 for the rectangle tool, vendored unchanged under `web/static/vendor/leaflet-geoman/`. leaflet-draw is removed.

## Consequences

- Epic 7 draws polygons with the same library and adds no new dependency.
- The Geoman toolbar also gives edit, drag and removal modes for the rectangle.
- The minified Geoman file is 297,727 bytes. It is the largest vendored file, and it takes most of the binary size that the vendored assets add.
- A Geoman upgrade must update `web/static/vendor/LICENSES.md`. `web/assets_test.go` fails until the SHA-256 values match.
- Only the free edition is vendored. A Geoman Pro feature needs a new decision, because Pro has a commercial license.
