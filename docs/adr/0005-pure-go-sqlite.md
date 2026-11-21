# 0005 — Use a pure-Go SQLite driver

Date: 2026-11-21
Status: accepted

## Context

The output writer uses `mattn/go-sqlite3`, which needs CGO (`internal/mbtiles/mbtiles.go:10`). The README cross-compile commands fail without a C cross-compiler. On 2026-11-21, `go run ./cmd/atak-dl` also failed at the CGO link step on macOS 27 (`ld: … tapi error: malformed file`). GoReleaser and a distroless image both need `CGO_ENABLED=0`.

Options compared on 2026-11-21:

- `modernc.org/sqlite` v1.62.0. It is a v1 driver, widely used, and has frequent releases.
- `ncruces/go-sqlite3` v0.38.0. It is pre-1.0 and uses more memory per connection.

Both are about 1.6 to 2 times slower than mattn at bulk inserts (https://github.com/cvilsmeier/go-sqlite-bench). The network is the bottleneck for a job.

## Decision

The project uses `modernc.org/sqlite` and builds with `CGO_ENABLED=0` on every target.

## Consequences

- One `go build` command builds all six release targets.
- The binary is larger. Measure the size in Epic 0.
- The driver name changes from `sqlite3` to `sqlite`, and pragmas move into the DSN.
- If modernc does not support a target, `ncruces/go-sqlite3` is the fallback.
