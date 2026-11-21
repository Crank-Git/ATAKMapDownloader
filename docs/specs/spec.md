---
name: ATAK Map Downloader
slug: atak-map-downloader
repo: Crank-Git/ATAKMapDownloader
status: approved
spec_version: 2
created: 2026-11-21
approved: 2026-11-21
html_generated: 2026-11-21
branch_model: dev-and-live
dev_branch: develop
visibility: public
features:
  - id: foundation
    file: features/00-foundation.md
  - id: map-sources
    file: features/01-map-sources.md
  - id: area-estimate
    file: features/02-area-estimate.md
  - id: tile-download
    file: features/03-tile-download.md
  - id: web-ui
    file: features/04-web-ui.md
  - id: security-hardening
    file: features/05-security-hardening.md
  - id: polite-downloading
    file: features/06-polite-downloading.md
  - id: robust-downloads
    file: features/07-robust-downloads.md
  - id: offline-web-ui
    file: features/08-offline-web-ui.md
  - id: source-sync
    file: features/09-source-sync.md
  - id: output-formats
    file: features/10-output-formats.md
  - id: area-selection
    file: features/11-area-selection.md
  - id: team-server
    file: features/12-team-server.md
---

# ATAK Map Downloader

## Overview

ATAK Map Downloader is a single Go binary, `atak-dl`. It reads MOBAC source files and downloads the tiles of one map source over an area and a zoom range. It writes the tiles to an SQLite output file that ATAK imports for offline use. The project has a CLI for scripts and a web UI for interactive area selection.

The project was written in February 2026 and received one outside fix in August 2026 (PR #3). This spec was reconstructed from the code on 2026-11-21. Features 01–04 describe what is built. Features 00 and 05–12 are the roadmap that issue-flow builds.

The project exists because ATAK users often work with no network. Examples are search and rescue teams, wildland fire crews and outdoor users. ATAK can stream tiles and download small areas on the device. This project moves that work to a desktop or a server: more bandwidth, larger areas, repeatable jobs, and one output file that many devices can use.

## Terms

| Term | Part of speech | Meaning in this project | Do not use |
|---|---|---|---|
| project | noun | ATAK Map Downloader as a whole, built as the `atak-dl` binary. | app, system, tool (in requirements) |
| ATAK | noun | Android Team Awareness Kit. Also covers iTAK and WinTAK when this spec says "TAK clients". | — |
| MOBAC | noun | Mobile Atlas Creator. Its XML format defines map sources. | — |
| map source | noun | One tile provider definition: name, URL template, zoom range, tile type. Code: `xmlparser.MapSource`. | provider, layer, map |
| source file | noun | One MOBAC XML file that defines one map source. | config, definition |
| source catalog | noun | The set of map sources that the project loaded. | library, source list |
| snapshot | noun | The pinned copy of ATAK-Maps source files embedded in the binary (planned, feature 09). | bundle, vendor copy |
| overlay | noun | A map source with transparent tiles, drawn on top of another map source. | layer |
| policy class | noun | The terms-of-use class of a map source: `open`, `conditional` or `restricted` (planned, feature 06). | license, tier |
| rate limit | noun | The maximum number of requests per second that the project sends to one host. | throttle |
| tile | noun | One square raster image at one zoom level, column and row. | image, chunk |
| zoom level | noun | An integer from 0 to 25 that selects the tile grid. | level, z (in prose) |
| zoom range | noun | A minimum and a maximum zoom level, both included. | levels |
| bounds | noun | A geographic rectangle: minimum and maximum latitude and longitude. The `--bbox` flag name stays as it is. | bbox (in prose), extent, box |
| area | noun | The region that a job covers: bounds, a polygon or a route buffer (polygon and route buffer are planned, feature 11). | AOI, region, selection |
| route buffer | noun | An area made from a line and a distance in metres on each side of it (planned). | corridor |
| estimate | noun | A prediction of the tile count, file size and time for an area and a zoom range. The `estimate` command name stays as it is. | quote, calculation |
| preview | noun | A grid of sample tiles from a map source, shown on the map over the area. | thumbnail |
| download | verb | To get tiles from a map source over HTTP and write them to an output file. Never a noun in this spec. | fetch, grab, scrape, pull |
| job | noun | One run of a download: map source, area, zoom range, settings, output file and state. | download (noun), task, run |
| output file | noun | The SQLite or zip file that a job writes. | tileset, database, map file, archive |
| output format | noun | The schema of the output file: `mbtiles`, `osmdroid` or `gpkg` (planned, feature 10). | file type |
| data package | noun | An ATAK zip file with a `MANIFEST/manifest.xml` (planned, feature 10). | mission package (except in ATAK quotes) |
| retry | verb | To send a tile request again after it fails. | re-fetch |
| resume | verb | To continue a job into an existing output file and skip the tiles it already holds (planned, feature 07). | restart, continue |
| failure rate | noun | Failed tiles divided by processed tiles in one job. | error rate |
| failure tolerance | noun | The highest failure rate at which a job still succeeds. Code: `MaxFailureRate`. | error budget |
| server | noun | The HTTP process that `atak-dl serve` starts. | backend, daemon |
| web UI | noun | The browser page that the server sends from the binary. | frontend, dashboard, web app |
| CLI | noun | Command-line interface: the `atak-dl` subcommands. | terminal mode |
| base map | noun | The background map that the web UI draws under the area. | basemap layer |
| geocode | verb | To find coordinates for a place name through Nominatim. | search (as a verb for this) |
| token | noun | The shared secret that a client sends to a server that is not bound to loopback (planned, feature 12). | password, API key |
| TMS | noun | Tile Map Service row order: row 0 is at the south edge. | — |
| XYZ | noun | Slippy-map row order: row 0 is at the north edge. | — |
| WMS | noun | OGC Web Map Service. A map source that the project requests with bounds instead of a tile column and row. | — |
| SSE | noun | Server-Sent Events: a one-way HTTP event stream (planned, feature 05). | — |

## Goals

1. A user prepares an offline output file for an area with one command or one web UI session.
2. Every output file that the project writes imports into ATAK with no manual change.
3. The binary builds for Linux, macOS and Windows on amd64 and arm64 with no C toolchain (`CGO_ENABLED=0`).
4. The web UI works with no internet connection, apart from the tile requests and the online base map.
5. The project sends no request that breaks a published usage policy by default: identifying User-Agent, rate limits, backoff, and a warning for restricted map sources.
6. A cancelled or failed job keeps every tile that it downloaded, and the user can resume it.
7. A team runs the server on one LAN machine and submits jobs from a browser, with a token.

## Non-goals

- The project does not host or proxy tiles to ATAK devices for live streaming.
- The project does not create vector tiles or render OpenStreetMap data.
- The project does not change the projection of raster tiles. Every map source is Web Mercator or a WMS that supports EPSG:3857.
- The project does not re-encode tiles to JPEG or WebP in v1.
- The project does not have user accounts. One shared token protects the server.
- The project does not keep a Python-version compatibility promise. The README claim is retired.
- The project does not ship a desktop wrapper (Wails or similar).

## Users & personas

| Persona | What they need | What they may do |
|---|---|---|
| Solo operator | Prepares offline maps for their own ATAK device before a trip. Uses the web UI or the CLI on a laptop. | Every action. The server is on loopback, so no token is needed. |
| Team preparer | Prepares output files for a SAR unit, a fire crew or a club, and gives them to members as files or data packages. | Every action. Often scripts the CLI. |
| Server operator | Runs `atak-dl serve` on a LAN machine or in Docker for a team. | Sets the bind address, the token, the data directory and the worker count. |
| Team member | Opens the server's web UI from a browser on the LAN. | With the token: submits jobs, cancels jobs and downloads output files. Without the token: nothing. |
| OSS user | Finds the project on GitHub. Installs it from a release archive or Docker. | Same as the solo operator. |

## Feature map

| Feature set | Spec file | Epic | Status | Mockups |
|---|---|---|---|---|
| Foundation | [features/00-foundation.md](features/00-foundation.md) | Epic 0: Foundation | planned | — |
| Map source catalog | [features/01-map-sources.md](features/01-map-sources.md) | Existing (built) | built | — |
| Area estimate and tile info | [features/02-area-estimate.md](features/02-area-estimate.md) | Existing (built) | built | — |
| Tile download and output file | [features/03-tile-download.md](features/03-tile-download.md) | Existing (built) | built | — |
| Web UI and server | [features/04-web-ui.md](features/04-web-ui.md) | Existing (built) | built | — |
| Security hardening | [features/05-security-hardening.md](features/05-security-hardening.md) | Epic 1: Security hardening | planned | — |
| Polite downloading | [features/06-polite-downloading.md](features/06-polite-downloading.md) | Epic 2: Polite downloading | planned | 04-source-policy-warning |
| Robust downloads | [features/07-robust-downloads.md](features/07-robust-downloads.md) | Epic 3: Robust downloads | planned | — |
| Offline web UI | [features/08-offline-web-ui.md](features/08-offline-web-ui.md) | Epic 4: Offline web UI | planned | 01-workspace |
| Source sync and WMS | [features/09-source-sync.md](features/09-source-sync.md) | Epic 5: Source sync and WMS | planned | — |
| Output formats | [features/10-output-formats.md](features/10-output-formats.md) | Epic 6: Output formats | planned | 03-export-options |
| Area selection | [features/11-area-selection.md](features/11-area-selection.md) | Epic 7: Area selection | planned | 02-area-tools |
| Team server | [features/12-team-server.md](features/12-team-server.md) | Epic 8: Team server | planned | 05-jobs |

## Architecture & stack

The diagram shows the target architecture after Epic 8. Components that exist today are marked "built".

```mermaid
flowchart LR
  subgraph Binary["atak-dl binary"]
    CLI["CLI commands (built)"]
    Server["Server: net/http (built, reworked)"]
    WebUI["Web UI: embedded page (built, reworked)"]
    Catalog["Source catalog: xmlparser (built)"]
    Snapshot["Snapshot: embedded ATAK-Maps (planned)"]
    Policy["Source policy table (planned)"]
    TileCalc["tilecalc: tile math (built)"]
    Engine["Download engine (built, reworked)"]
    Writer["Output writer: mbtiles / osmdroid / gpkg (built mbtiles)"]
    Jobs["Job queue and store (planned)"]
  end
  Browser["Browser"] -->|HTTP + SSE| Server
  Server --> WebUI
  CLI --> Engine
  Server --> Jobs --> Engine
  Snapshot --> Catalog
  SourceDir["--mapsources-dir"] --> Catalog
  Catalog --> Engine
  Policy --> Engine
  TileCalc --> Engine
  Engine -->|rate-limited HTTPS| Tiles["Tile servers"]
  Engine --> Writer --> Out["Output file"]
  Server -->|1 request/s| Nominatim["Nominatim geocoder"]
  Out -->|copy or data package| TAK["ATAK device"]
```

Per component:

- **Language and runtime: Go.** Today `go.mod` says `go 1.21`, and the installed toolchain is 1.27.3. Epic 0 sets `go 1.26` with `toolchain go1.27.4`. Go 1.27.4 is the current stable release (https://go.dev/doc/devel/release, retrieved 2026-11-21). Go beat the alternatives because it gives one static binary and has a cross-compile story that a solo maintainer can keep. See [ADR 0001](../adr/0001-go-single-binary-cli.md).
- **CLI: spf13/cobra.** Built. Cobra is kept because it already defines every command.
- **SQLite driver: modernc.org/sqlite (planned).** It replaces `mattn/go-sqlite3`, which needs CGO. modernc is at v1.62.0 (https://pkg.go.dev/modernc.org/sqlite?tab=versions, retrieved 2026-11-21). It is about 1.6 times slower than mattn at bulk inserts (https://github.com/cvilsmeier/go-sqlite-bench). The network is the bottleneck, so the difference is small. ncruces/go-sqlite3 is pre-1.0 and is the fallback. See [ADR 0005](../adr/0005-pure-go-sqlite.md).
- **Server: net/http.** The Go 1.22 method-and-path patterns replace the hand-written router in `cmd/atak-dl/commands/web.go:52-75`. `http.CrossOriginProtection` (Go 1.25, https://go.dev/doc/go1.25) blocks requests from other web pages. `os.Root` (Go 1.24) limits file serving to the output directory.
- **Progress channel: SSE (planned).** SSE replaces `gorilla/websocket`, which has had no release since v1.5.3 in June 2024 (https://pkg.go.dev/github.com/gorilla/websocket?tab=versions). Progress goes from the server to the browser only, and browser commands become POST requests. See [ADR 0006](../adr/0006-sse-job-api.md).
- **Web UI: vanilla JavaScript + Leaflet 1.9.4 + Leaflet-Geoman (planned), embedded with `embed.FS`.** Leaflet 2.0 is still in beta (https://github.com/Leaflet/Leaflet/releases). Leaflet.draw has had no release since 2018, and Leaflet-Geoman v2.20.8 is active (https://github.com/geoman-io/leaflet-geoman/releases). Bootstrap and Font Awesome are removed. All assets are vendored, so the page works offline.
- **Map sources: MOBAC XML from joshuafuller/ATAK-Maps (MIT).** Today there is a copied folder of 33 files. Planned: an embedded snapshot of ATAK-Maps v2.3.1 with 53 source files (https://github.com/joshuafuller/ATAK-Maps/releases). See [ADR 0004](../adr/0004-mobac-xml-sources.md) and [ADR 0008](../adr/0008-embedded-source-snapshot.md).
- **Release: GoReleaser v2 on GitHub Actions (planned).** Release targets are GitHub Releases and a GHCR image. GoReleaser v2.20.3 is current (https://github.com/goreleaser/goreleaser/releases). The project publishes no Homebrew tap, to keep the release surface small.
- **Lint: golangci-lint v2 (planned).** Version v2.15.0, with the complexity linters enabled (https://github.com/golangci/golangci-lint/releases).

Repository layout (current, plus planned additions in italics):

```
cmd/atak-dl/            CLI entry point and one file per command
internal/xmlparser/     source file parser (built)
internal/tilecalc/      Web Mercator tile math (built)
internal/download/      download engine (built)
internal/mbtiles/       MBTiles writer (built)
internal/output/        planned: one writer per output format
internal/policy/        planned: source policy table and rate limits
internal/jobs/          planned: job queue and job store
internal/server/        planned: HTTP handlers moved out of commands/web.go
web/                    embedded web UI
mapsources/             source files (replaced by the embedded snapshot in Epic 5)
docs/specs/ docs/adr/   this spec and the decision records
```

## Data model

```mermaid
erDiagram
  MAP_SOURCE ||--o| SOURCE_POLICY : "matched by host"
  MAP_SOURCE ||--o{ JOB : "used by"
  JOB ||--|{ AREA : covers
  JOB ||--|| OUTPUT_FILE : writes
  OUTPUT_FILE ||--|{ TILE : holds
  OUTPUT_FILE ||--|{ METADATA_ENTRY : holds
  OUTPUT_FILE ||--o{ FAILED_TILE : records
  JOB ||--o{ JOB_EVENT : emits
  SOURCE_FILE ||--o| MAP_SOURCE : defines
  SOURCE_FILE ||--o| SKIPPED_SOURCE : "fails as"
  MAP_SOURCE {
    string name PK
    string url
    int min_zoom
    int max_zoom
    string tile_type
    string tile_update
    string background_color
    bool ignore_errors
    string_list server_parts
    string kind "tile or wms (planned)"
    bool invert_y "planned"
  }
  SOURCE_FILE {
    string path PK
    string origin "snapshot or user (planned)"
  }
  SKIPPED_SOURCE {
    string path FK
    string reason "planned"
    string detail
  }
  SOURCE_POLICY {
    string host_pattern PK
    string policy_class
    float requests_per_second
    string reason
    string reference_url
  }
  JOB {
    string id PK "planned"
    string source_name FK
    int min_zoom
    int max_zoom
    string state
    string output_format "planned"
    int total
    int downloaded
    int skipped "planned"
    int failed
    time created_at "planned"
  }
  AREA {
    string kind "bounds, polygon, route_buffer"
    string geometry
    int min_zoom
    int max_zoom
  }
  OUTPUT_FILE {
    string path PK
    string format
  }
  TILE {
    int zoom_level
    int tile_column
    int tile_row "TMS"
    blob tile_data
  }
  METADATA_ENTRY {
    string name PK
    string value
  }
  FAILED_TILE {
    int zoom_level
    int tile_column
    int tile_row
    string reason "planned"
  }
  JOB_EVENT {
    string type
    string payload
  }
```

Per entity:

- **MAP_SOURCE** (built, `internal/xmlparser/xmlparser.go:16-26`). The parser loads it from a `<customMapSource>` source file. `name` is the key. If two source files have the same name, the last file loaded wins with no warning. Defaults: if both zooms are 0, `max_zoom` is 18. `tile_type` is `png`, `tile_update` is `None` and `background_color` is `#000000`. Planned: `kind` (`tile` or `wms`) and `invert_y` (feature 09).
- **SOURCE_FILE** (built). A MOBAC XML file in the source directory. Planned: `origin` is `snapshot` or `user` (feature 09).
- **SKIPPED_SOURCE** (planned, feature 09). An in-memory record of a source file that did not load, with its reason. Today the parser drops these with no record.
- **SOURCE_POLICY** (planned, feature 06). A table embedded in the binary, keyed by a host pattern such as `*.google.com`. `policy_class` is `open`, `conditional` or `restricted`. `requests_per_second` is the rate limit for that host.
- **JOB** (built as one in-memory record, `cmd/atak-dl/commands/web.go:30-34`). The state is `idle` or `downloading`. Planned (features 05 and 12): an `id`, the states in the state diagram below, and a persistent record in the data directory.
- **AREA** (built as bounds only). Planned: polygon and route buffer geometry as GeoJSON, with a zoom range for each area (feature 11).
- **OUTPUT_FILE** (built). Path: `./downloads/<name>_z<min>-<max>.sqlite`. An existing file at that path is deleted first, and the file is deleted again on cancel. Planned: an extension per output format, no silent delete, and resume (features 07 and 10).
- **TILE** (built, `internal/mbtiles/mbtiles.go:29-34`). `UNIQUE (zoom_level, tile_column, tile_row)`. `tile_row` is TMS: `(1<<z)-1-y`. `tile_data` holds the bytes the server returned, with no check.
- **METADATA_ENTRY** (built). Keys: `name`, `type`, `version`, `description`, `format`, `bounds`, `minzoom`, `maxzoom`. Planned: `center` and `attribution` (feature 10).
- **FAILED_TILE** (planned, feature 07). A table `atakdl_failed_tiles` in the output file. ATAK reads only the `tiles` and `metadata` tables, so an extra table is safe. This is inferred from ATAK-CIV `TilesetInfo.java` and is a risk item.
- **JOB_EVENT** (planned). An SSE message: `progress`, `state` or `log`.

Job lifecycle (planned, features 05, 07 and 12):

```mermaid
stateDiagram-v2
  [*] --> queued
  queued --> running : worker free
  queued --> cancelled : cancel
  running --> complete : failure rate within failure tolerance
  running --> failed : failure rate above failure tolerance or auto-stop
  running --> cancelled : cancel
  failed --> queued : resume
  cancelled --> queued : resume
  complete --> [*]
```

## Cross-cutting concerns

- **Authentication and authorization.** Today there is none, and that is acceptable only on loopback. Planned (feature 12): if the server binds to an address that is not loopback, every request must carry the token. The token comes from `--token` or `ATAKDL_TOKEN`. If neither is set, the server generates one and prints it once at start.
- **Cross-origin requests.** Today the server sends `Access-Control-Allow-Origin: *`, and the websocket accepts any origin (`web.go:44,54`). Any web page that the user visits can start a job. Planned (feature 05): no CORS headers, and `http.CrossOriginProtection` on every state-changing route.
- **TLS.** Today `InsecureSkipVerify` is true by default (`internal/download/download.go:42,100`). Planned (feature 05): verification is on by default, and `--insecure` turns it off for one job.
- **Error handling.** Today handlers write hand-built JSON strings as `text/plain` (`web.go:179,352`). Planned (feature 05): every error response is `application/json` with the shape `{"error": {"code": "<snake_case>", "message": "<text>"}}`.
- **Validation.** Planned limits:
  - The zoom range is within the map source's zoom range.
  - The tile count is at most `--max-tiles`, with a default of 500,000 (feature 07).
  - A request body is at most 1 MiB.
  - Concurrency is from 1 to 32.
- **Logging.** `log/slog` with the text handler today. Planned: every job log line carries `job_id`, and every failed tile is logged at debug level with its URL and reason.
- **Politeness.** Planned (feature 06):
  - The User-Agent is `atak-map-downloader/<version> (+https://github.com/Crank-Git/ATAKMapDownloader)`.
  - Each host has a rate limit.
  - The project backs off on 429 and 503 and honours `Retry-After`.
  - Geocoding is limited to 1 request per second across the whole server, as the Nominatim policy requires (https://operations.osmfoundation.org/policies/nominatim/).
- **Accessibility.** Planned (feature 08):
  - Every control has a label.
  - Icon buttons have `aria-label`.
  - The job status region is `aria-live="polite"`.
  - The progress bar sets `aria-valuenow`.
  - The theme follows `prefers-color-scheme`.
- **Performance targets.** The tile writer stores at least 500 tiles per second on a laptop SSD (one writer goroutine, one transaction per 500 tiles). The server starts in less than 1 second with the embedded snapshot.
- **Internationalization.** English only. No i18n is planned.

## Environments & config

- **Environments.** One environment: the user's machine, or a LAN server or container. The project has no hosted deployment. Releases are the deploy target: GitHub Releases and `ghcr.io/crank-git/atak-map-downloader`.
- **Configuration.** Configuration is through flags today, and no code reads an environment variable. Planned environment variables:

| Name | Feature | Meaning |
|---|---|---|
| `ATAKDL_TOKEN` | 12 | The token for a server that is not on loopback. It has the same meaning as `--token`. |
| `ATAKDL_DATA_DIR` | 12 | The directory for output files and the job store. It has the same meaning as `--data-dir`. The default is `./downloads`. |

- **CI secrets** (names only; see [docs/external.md](../external.md)): `GITHUB_TOKEN` (provided by Actions). No other secret is needed.
- **Seed and demo data.** Epic 0 adds `testdata/` with three fixture source files (tile, quadkey and WMS) and a fake tile server for tests (`internal/testtiles`). The demo is `atak-dl download --source "OpenTopo - Opentopomap" --bbox 45.30,-121.80,45.45,-121.60 --min-zoom 8 --max-zoom 12`, Mount Hood with about 60 tiles.

## Testing strategy

- **Unit tests.** `go test -race ./...`. Every package in `internal/` has table-driven tests. Today only `tilecalc` and `xmlparser` have tests (12 tests, all pass).
- **Integration tests.** The download engine and every output writer run against `internal/testtiles`, an `httptest` tile server. The test server can return 200, 404, 429 with `Retry-After`, 503, HTML bodies and slow responses. No test calls a real tile server.
- **Conformance tests.** Each output format has a test that opens the output file and applies ATAK's format-detection rules from ATAK-CIV `TilesetInfo.java`: the `tiles` column set, the row order and the metadata keys. A device check is a manual, optional step, because the maintainer has no TAK client installed.
- **HTTP tests.** The server handlers run under `httptest`. SSE and timeouts use `testing/synctest`.
- **Smoke test.** `scripts/smoke.sh` builds the binary and starts `serve` on a free port. It checks that `GET /` returns 200 and that `GET /api/sources` returns a non-empty list, then stops the server.
- **Lint.** `golangci-lint run` with the config from Epic 0. `gofmt -l .` prints nothing.
- **What "done" means for a change:** `go test -race ./...`, `golangci-lint run` and `scripts/smoke.sh` pass in CI on the PR. A change to an output format also passes its conformance test.

## Epics

1. **Epic 0: Foundation.** Goal: a fresh clone tests, lints, builds and releases with no C toolchain. Covers [00-foundation](features/00-foundation.md). Depends on: nothing. Exit: CI is green on `develop`, and a `v0.2.0-rc` tag produces signed release artifacts.
2. **Epic 1: Security hardening.** Goal: no web page and no network attacker can drive or tamper with a job. Covers [05-security-hardening](features/05-security-hardening.md). Depends on: Epic 0. Exit: the cross-origin, TLS and limit tests pass, and the websocket is gone.
3. **Epic 2: Polite downloading.** Goal: default behavior meets the published tile and Nominatim policies. Covers [06-polite-downloading](features/06-polite-downloading.md). Depends on: Epic 1 (the job API). Exit: rate-limit and backoff tests pass, and restricted map sources need confirmation.
4. **Epic 3: Robust downloads.** Goal: no job loses a downloaded tile, and no output file holds a bad tile. Covers [07-robust-downloads](features/07-robust-downloads.md). Depends on: Epic 2 (the host rate limiter that the engine waits on). Exit: resume, validation and tile-cap tests pass under `-race`.
5. **Epic 4: Offline web UI.** Goal: the web UI works air-gapped and returns output files to the browser. Covers [08-offline-web-ui](features/08-offline-web-ui.md). Depends on: Epics 1 and 3 (the job panel shows `skipped` and sends `if_exists`). Exit: the page loads with the network off, and no request leaves for a CDN.
6. **Epic 5: Source sync and WMS.** Goal: the binary carries a current source catalog, and every source file in it loads or reports why not. Covers [09-source-sync](features/09-source-sync.md). Depends on: Epic 2 (policy table keys). Exit: 53 snapshot source files load or are listed as skipped with a reason.
7. **Epic 6: Output formats.** Goal: the user chooses MBTiles, OSMDroid or GeoPackage, optionally inside a data package. Covers [10-output-formats](features/10-output-formats.md). Depends on: Epic 3 (the single writer). Exit: the conformance tests pass for all three output formats.
8. **Epic 7: Area selection.** Goal: a job covers polygons, imported files and route buffers, with a zoom range for each area. Covers [11-area-selection](features/11-area-selection.md). Depends on: Epic 4 (Geoman). Exit: a polygon job downloads only intersecting tiles.
9. **Epic 8: Team server.** Goal: a team shares one server with a token, a job queue and job history. Covers [12-team-server](features/12-team-server.md). Depends on: Epics 1 and 4. Exit: the Docker image runs headless and two queued jobs complete in order.

## Milestones

| Milestone | Epics | What "shippable" means |
|---|---|---|
| v0.2.0: Modern build | 0 | Signed CGO-free binaries for 6 platforms, and a GHCR image. Behavior is unchanged. |
| v0.3.0: Safe by default | 1, 2 | The local server cannot be driven by other web pages. TLS is verified. Requests follow the tile and Nominatim policies. |
| v0.4.0: Reliable jobs | 3, 4 | Resume, tile validation and tile caps. The web UI works offline and returns output files. |
| v0.5.0: Current catalog | 5, 6 | The ATAK-Maps v2.3.1 snapshot with WMS support. Three output formats and data packages. |
| v1.0.0: Teams | 7, 8 | Polygon and route areas. A token-protected team server with a queue. |

After Epic 3, issue-flow may build Epics 4, 5 and 6 in parallel.

## Assumptions

1. The default output format stays MBTiles, and the default extension changes from `.sqlite` to `.mbtiles` in Epic 6. ATAK detects the format by the schema, not by the extension (ATAK-CIV `TilesetInfo.java`), so the change is safe for import.
2. The tile cap default is 500,000 tiles per job. That is about 7.5 GB at the CLI's 15 KB per tile estimate.
3. The default rate limit for a host that is not in the policy table is 4 requests per second, with concurrency at most 4 for that host.
4. Restricted map sources stay in the snapshot. They are labeled, rate-limited to 1 request per second, and need explicit confirmation (`--accept-terms` in the CLI, a checkbox in the web UI). This is the user's "Tag + warn" choice.
5. The team server runs jobs one at a time by default (`--workers 1`), because tile servers rate-limit by client IP.
6. The job store is a JSON-lines file in the data directory, not an SQLite database. Job volume is small.
7. Release versions start at v0.2.0. The current code is v0.1.0 for history, and no tag exists today.
8. Device verification on ATAK is optional and manual. Conformance tests carry the acceptance criteria.

## Risks & open questions

Reconciliation of the existing documentation against the code (Phase 1):

- **Contradicted — README cross-compile.** `README.md:37-46` tells users to cross-compile with `GOOS`/`GOARCH`. The `mattn/go-sqlite3` driver needs CGO, so those commands fail with no C cross-compiler. The code wins. Epic 0 fixes the driver and the README.
- **Contradicted — "1:1 functionality matching the Python version".** `README.md:5`. No Python code is in this repo, and behavior differs: WMS is skipped, and the defaults for failure tolerance and size per tile are different. The user retired the claim. Epic 0 removes it.
- **Contradicted — "30+ map sources".** 33 files ship. 30 load. Three WMS source files are skipped with no log (`internal/xmlparser/xmlparser.go:129-132`).
- **Contradicted — "MBTiles output".** The output file has a `.sqlite` extension and lacks the `center` key that the MBTiles 1.3 spec recommends (https://github.com/mapbox/mbtiles-spec/blob/master/1.3/spec.md). ATAK loads it through its "momap" path, not its "mbtiles" path.
- **Contradicted — "Batch Delay".** The web UI offers a batch delay (`web/index.html:127`). The engine never reads `DelayBetweenBatches` (`internal/download/download.go:28`).
- **Risk — the build fails on this machine today.** On 2026-11-21, `go run ./cmd/atak-dl` failed at the CGO link step on macOS 27 (`ld: … tapi error: malformed file … unknown architecture`). The prebuilt `./atak-dl` from February still runs. Epic 0 removes CGO (ADR 0005), so this is the first sub-issue to build.
- **Risk — a late cancel is lost.** The engine checks the context only while it starts tile requests (`internal/download/download.go:171-184`). A SIGINT after the last request starts ends as a normal job, and the CLI can print "Download complete". Feature 07 fixes this (FR-robust-downloads-30).
- **Risk — preview tiles can fail to show.** The preview overlays set `crossOrigin: 'anonymous'` (`web/static/js/app.js`), so a tile server that sends no CORS header blocks the image. Feature 08 owns the preview rendering.
- **Risk — ATAK format handling is inferred, not tested.** The public ATAK-CIV source is archived at 4.6.0.5 (October 2024). No public source exists for ATAK 5.x, iTAK, WinTAK or TAK Aware. Each output format can fail on a newer client. Mitigation: conformance tests, plus an optional device check before v0.5.0.
- **Risk — restricted map sources.** Google and Esri terms prohibit bulk download and offline caching. Google: https://developers.google.com/maps/documentation/tile/policies. Esri: https://www.esri.com/content/dam/arcgisonline/docs/tou_summary.pdf. The OSM tile policy says "Offline use is not permitted" (https://operations.osmfoundation.org/policies/tiles/). Microsoft says direct Bing tile URLs are "not allowed" (https://learn.microsoft.com/en-us/bingmaps/rest-services/directly-accessing-the-bing-maps-tiles). The project warns and rate-limits, and the user decides. Bing tile endpoints can stop working at any time.
- **Risk — the ATAK-Maps v2 rename.** ATAK-Maps v2.0.0 renamed every map source (https://github.com/joshuafuller/ATAK-Maps/blob/master/CHANGELOG.md). Scripts that pass `--source "Google - Satellite Only"` break after Epic 5. Mitigation: an alias table from old names to new names for one release.
- **Open question — Leaflet-Geoman license.** The free edition is believed to be MIT, but the research spike did not confirm it. Epic 4 confirms the license before it vendors the library.
- **Open question — WebP support in ATAK.** Not verified. Re-encoding is a non-goal for v1.
- **Open question — the OSMDroid `ATAK_metadata` table.** The exact schema must be read from ATAK-CIV `OSMDroidTileContainer.java` before Epic 6 builds the writer.
- **Undocumented behavior found in the code** (no document mentions it):
  - the `/api/preview` route and its tile grid
  - the `/api/geocode` Nominatim proxy
  - the `/api/tile-info` and `/api/validate-source` routes, which the web UI never calls
  - the auto-zoom rule in `estimate`
  - the mtime-based source cache
  - deletion of an existing output file before a job
  - deletion of the output file on cancel
  - the single global job lock
  - dark mode that inverts map tiles

## Issue map

Created by spec-to-issues on 2026-11-21. Features 01–04 are `built` and have no issues.

| Epic | Epic issue | Sub-issues |
|---|---|---|
| Foundation | #4 | #5, #6, #7, #8, #9, #10 |
| Security hardening | #11 | #12, #13, #14, #15, #16, #17 |
| Polite downloading | #18 | #19, #20, #21, #22, #23 |
| Robust downloads | #24 | #25, #26, #27, #28, #29 |
| Offline web UI | #30 | #31, #32, #33, #34, #35 |
| Source sync and WMS | #36 | #37, #38, #39, #40, #41, #42 |
| Output formats | #43 | #44, #45, #46, #47, #48, #49 |
| Area selection | #50 | #51, #52, #53, #54 |
| Team server | #55 | #56, #57, #58, #59, #60 |

FR coverage (each sub-issue and the requirements it quotes):

| Sub-issue | Issue | Requirements |
|---|---|---|
| Replace the CGO SQLite driver with modernc.org/sqlite | #5 | FR-foundation-1, FR-foundation-2, FR-foundation-3, FR-foundation-4 |
| Format the code and add golangci-lint v2 config | #6 | FR-foundation-5, FR-foundation-6, FR-foundation-7, FR-foundation-8 |
| Add the test tile server, fixtures and engine integration tests | #7 | FR-foundation-9, FR-foundation-10, FR-foundation-11, FR-foundation-12 |
| Add the smoke script and the CI workflow | #8 | FR-foundation-13, FR-foundation-14, FR-foundation-15, FR-foundation-16 |
| Add GoReleaser releases, the GHCR image and --version | #9 | FR-foundation-19, FR-foundation-20, FR-foundation-21, FR-foundation-22 |
| Create the develop branch and update the README build section | #10 | FR-foundation-17, FR-foundation-18, FR-foundation-23, FR-foundation-24 |
| Return JSON errors with one error shape | #12 | FR-security-hardening-21, FR-security-hardening-22, FR-security-hardening-23, FR-security-hardening-24 |
| Remove CORS headers and add cross-origin protection | #13 | FR-security-hardening-1, FR-security-hardening-2, FR-security-hardening-3 |
| Verify TLS by default and add the --insecure flag | #14 | FR-security-hardening-4, FR-security-hardening-5, FR-security-hardening-6 |
| Add the job API with an SSE event stream | #15 | FR-security-hardening-8, FR-security-hardening-9, FR-security-hardening-10, FR-security-hardening-11, FR-security-hardening-12, FR-security-hardening-13, FR-security-hardening-14, FR-security-hardening-15 |
| Move the web UI to the job API and remove the websocket | #16 | FR-security-hardening-7, FR-security-hardening-16 |
| Add request limits, server timeouts and input validation | #17 | FR-security-hardening-17, FR-security-hardening-18, FR-security-hardening-19, FR-security-hardening-20, FR-security-hardening-25 |
| Add the embedded policy table and match map sources to it | #19 | FR-polite-downloading-1, FR-polite-downloading-2, FR-polite-downloading-3, FR-polite-downloading-4, FR-polite-downloading-5, FR-polite-downloading-6, FR-polite-downloading-15, FR-polite-downloading-16 |
| Add per-host rate limits, backoff and the project User-Agent | #20 | FR-polite-downloading-7, FR-polite-downloading-8, FR-polite-downloading-9, FR-polite-downloading-10, FR-polite-downloading-11, FR-polite-downloading-12, FR-polite-downloading-13, FR-polite-downloading-14 |
| Require accepted terms for restricted map sources | #21 | FR-polite-downloading-17, FR-polite-downloading-18, FR-polite-downloading-19 |
| Add the policy warning dialog and source badges to the web UI | #22 | FR-polite-downloading-20, FR-polite-downloading-21 |
| Rate-limit and cache geocode requests to Nominatim | #23 | FR-polite-downloading-22, FR-polite-downloading-23, FR-polite-downloading-24, FR-polite-downloading-25, FR-polite-downloading-26 |
| Write tiles through one writer goroutine with batched transactions | #25 | FR-robust-downloads-1, FR-robust-downloads-2, FR-robust-downloads-3, FR-robust-downloads-4, FR-robust-downloads-5 |
| Refuse to overwrite output files and add resume | #26 | FR-robust-downloads-6, FR-robust-downloads-7, FR-robust-downloads-8, FR-robust-downloads-9, FR-robust-downloads-10, FR-robust-downloads-11, FR-robust-downloads-12, FR-robust-downloads-13, FR-robust-downloads-14 |
| Reject tiles that are not valid images | #27 | FR-robust-downloads-15, FR-robust-downloads-16, FR-robust-downloads-17, FR-robust-downloads-18, FR-robust-downloads-19, FR-robust-downloads-20 |
| Record failed tiles and run a retry pass | #28 | FR-robust-downloads-21, FR-robust-downloads-22, FR-robust-downloads-23, FR-robust-downloads-24, FR-robust-downloads-25 |
| Add the tile cap, processed-tile progress and fast cancel | #29 | FR-robust-downloads-26, FR-robust-downloads-27, FR-robust-downloads-28, FR-robust-downloads-29, FR-robust-downloads-30, FR-robust-downloads-31 |
| Vendor Leaflet and Geoman and remove every CDN asset | #31 | FR-offline-web-ui-1, FR-offline-web-ui-2, FR-offline-web-ui-3, FR-offline-web-ui-4, FR-offline-web-ui-5, FR-offline-web-ui-6 |
| Add the output file list and file download routes | #32 | FR-offline-web-ui-7, FR-offline-web-ui-8, FR-offline-web-ui-9, FR-offline-web-ui-10, FR-offline-web-ui-11, FR-offline-web-ui-12 |
| Add a local base map from an output file | #33 | FR-offline-web-ui-13, FR-offline-web-ui-14, FR-offline-web-ui-15, FR-offline-web-ui-16 |
| Add connection, loading and error states to the workspace | #34 | FR-offline-web-ui-17, FR-offline-web-ui-18, FR-offline-web-ui-19, FR-offline-web-ui-20, FR-offline-web-ui-21, FR-offline-web-ui-22, FR-offline-web-ui-23, FR-offline-web-ui-24 |
| Fix accessibility and theme behavior in the web UI | #35 | FR-offline-web-ui-25, FR-offline-web-ui-26, FR-offline-web-ui-27, FR-offline-web-ui-28, FR-offline-web-ui-29, FR-offline-web-ui-30, FR-offline-web-ui-31 |
| Embed the ATAK-Maps v2.3.1 snapshot with a user override | #37 | FR-source-sync-1, FR-source-sync-2, FR-source-sync-3, FR-source-sync-4, FR-source-sync-5, FR-source-sync-6, FR-source-sync-7, FR-source-sync-8, FR-source-sync-9 |
| Report skipped source files in the CLI, API and web UI | #38 | FR-source-sync-26, FR-source-sync-27, FR-source-sync-28, FR-source-sync-29, FR-source-sync-30, FR-source-sync-31 |
| Add the sources sync command | #39 | FR-source-sync-10, FR-source-sync-11, FR-source-sync-12, FR-source-sync-13, FR-source-sync-14 |
| Map old map source names to their ATAK-Maps v2 names | #40 | FR-source-sync-15, FR-source-sync-16, FR-source-sync-17 |
| Support WMS map sources | #41 | FR-source-sync-18, FR-source-sync-19, FR-source-sync-20 |
| Read invertYCoordinate, comma server parts and every placeholder | #42 | FR-source-sync-21, FR-source-sync-22, FR-source-sync-23, FR-source-sync-24, FR-source-sync-25 |
| Add the output writer interface and the --format flag | #44 | FR-output-formats-1, FR-output-formats-2, FR-output-formats-3, FR-output-formats-4, FR-output-formats-5 |
| Complete the MBTiles metadata and add its conformance test | #45 | FR-output-formats-6, FR-output-formats-7, FR-output-formats-8, FR-output-formats-9, FR-output-formats-10, FR-output-formats-25, FR-output-formats-26 |
| Add the OSMDroid writer | #46 | FR-output-formats-11, FR-output-formats-12, FR-output-formats-13, FR-output-formats-14, FR-output-formats-25 |
| Add the GeoPackage writer | #47 | FR-output-formats-15, FR-output-formats-16, FR-output-formats-17, FR-output-formats-18, FR-output-formats-25, FR-output-formats-27 |
| Export an output file as an ATAK data package | #48 | FR-output-formats-19, FR-output-formats-20, FR-output-formats-21, FR-output-formats-22, FR-output-formats-23 |
| Add the export panel to the web UI | #49 | FR-output-formats-24 |
| Select tiles that intersect a polygon or a route buffer | #51 | FR-area-selection-1, FR-area-selection-2, FR-area-selection-3, FR-area-selection-6, FR-area-selection-7 |
| Run jobs with several areas and a zoom range per area | #52 | FR-area-selection-4, FR-area-selection-5, FR-area-selection-8, FR-area-selection-9, FR-area-selection-10, FR-area-selection-25, FR-area-selection-26, FR-area-selection-27, FR-area-selection-28, FR-area-selection-29 |
| Read GeoJSON and KML areas in the CLI | #53 | FR-area-selection-11, FR-area-selection-12, FR-area-selection-13, FR-area-selection-14, FR-area-selection-15, FR-area-selection-16, FR-area-selection-17, FR-area-selection-18 |
| Add polygon, route and import tools to the web UI | #54 | FR-area-selection-19, FR-area-selection-20, FR-area-selection-21, FR-area-selection-22, FR-area-selection-23, FR-area-selection-24 |
| Queue jobs and run them on a worker pool | #56 | FR-team-server-1, FR-team-server-2, FR-team-server-3, FR-team-server-4, FR-team-server-5, FR-team-server-6 |
| Persist jobs in jobs.jsonl and add the data directory | #57 | FR-team-server-7, FR-team-server-8, FR-team-server-9, FR-team-server-10, FR-team-server-11, FR-team-server-12, FR-team-server-13, FR-team-server-14, FR-team-server-15 |
| Require a token for a server that is not on loopback | #58 | FR-team-server-16, FR-team-server-17, FR-team-server-18, FR-team-server-19, FR-team-server-20, FR-team-server-21, FR-team-server-22, FR-team-server-23, FR-team-server-24 |
| Add the Jobs page and the sign-in page | #59 | FR-team-server-25, FR-team-server-26, FR-team-server-27, FR-team-server-28 |
| Add the health route and the Docker server guide | #60 | FR-team-server-29, FR-team-server-30, FR-team-server-31 |

## Changelog

- 2026-11-21: Created by project-inherit. Features 01–04 are built and documented from the code. Features 00 and 05–12 are planned from the roadmap interview.
- 2026-11-21: Approved by the maintainer after review round 1. Mockups 02, 03 and 04 were adjusted for heading sizes and borders.
- 2026-11-21: Removed the Homebrew tap from the release targets, at the maintainer's request, to make releases simpler.
- 2026-11-21: Issued by spec-to-issues: 9 epics (#4, #11, #18, #24, #30, #36, #43, #50, #55) and 48 sub-issues. Epic 4 now depends on Epic 3.
