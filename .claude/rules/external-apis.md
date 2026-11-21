---
paths:
  - "**/*.go"
  - "web/**/*.{js,html}"
  - ".goreleaser.yaml"
  - ".github/**"
  - "docs/specs/**/*.md"
---

# External APIs: read the documentation, never assume

Before you write code against any interface below, confirm the operation, the parameters, the response shape, the error cases and the limits from its documentation. Cite the URL and the version in the PR body, for example: `Verified against: https://www.geopackage.org/spec131/ (retrieved 2026-12-01)`. If the documentation contradicts the spec, stop and mark the issue `needs-feedback`. Do not reconcile by guessing.

| Interface | Version pinned | Documentation |
|---|---|---|
| ATAK-CIV format readers (`TilesetInfo.java`, `OSMDroidTileContainer.java`, `OSMUtils.java`, `GeoPackageLayerInfoSpi.java`) | archived 4.6.0.5 | https://github.com/deptofdefense/AndroidTacticalAssaultKit-CIV/tree/main/takkernel/engine/src/main/java/com/atakmap/ |
| MBTiles | 1.3 | https://github.com/mapbox/mbtiles-spec/blob/master/1.3/spec.md |
| OGC GeoPackage | 1.3.1 | https://www.geopackage.org/spec131/ |
| OGC WMS GetMap | 1.3.0 | https://www.ogc.org/standards/wms |
| ATAK-Maps source files and XML reference | v2.3.1 | https://github.com/joshuafuller/ATAK-Maps/blob/master/docs/xml-reference.md |
| Nominatim usage policy | current | https://operations.osmfoundation.org/policies/nominatim/ |
| OSM tile usage policy | current | https://operations.osmfoundation.org/policies/tiles/ |
| GitHub REST releases | current | https://docs.github.com/en/rest/releases/releases |
| modernc.org/sqlite | v1.62.0 | https://pkg.go.dev/modernc.org/sqlite |
| Go standard library (`net/http`, `os.Root`, `CrossOriginProtection`) | Go 1.27 | https://pkg.go.dev/std |
| golang.org/x/time/rate | latest | https://pkg.go.dev/golang.org/x/time/rate |
| GoReleaser | v2.20 | https://goreleaser.com/customization/ and https://goreleaser.com/deprecations/ |
| golangci-lint config | v2.15 | https://golangci-lint.run/docs/configuration/ |
| Leaflet | 1.9.4 | https://leafletjs.com/reference.html |
| Leaflet-Geoman | v2.20.8 | https://github.com/geoman-io/leaflet-geoman (confirm the license before you vendor it) |

Tests never call a real tile server, Nominatim or GitHub. Use `httptest` fakes. A provider's terms of use can change, so re-read the reference before you change an entry in the policy table.

Creating or changing GHCR packages or release tags is an outward-facing action. Confirm it with the maintainer first.
