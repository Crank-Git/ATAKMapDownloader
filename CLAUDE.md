# ATAK Map Downloader

ATAK Map Downloader (`atak-dl`) is one Go binary. It reads MOBAC source files, downloads the tiles of one map source over an area and a zoom range, and writes an output file that ATAK imports for offline use. It has a CLI and an embedded web UI (`atak-dl serve`).

Current state: v0.1 as built in February 2026. The roadmap is Epics 0–8 in the spec. Epic 0 (Foundation) is not built yet, so CI, lint config and the CGO-free build do not exist until it lands.

Design lives in `docs/specs/`. Read `docs/specs/spec.md` and the relevant `docs/specs/features/*.md` before you build a feature. Use the words in the `## Terms` table of `docs/specs/spec.md`. Decisions are in `docs/adr/`. External facts (environment variable names, secrets by name, third-party policies) are in `docs/external.md`.

## Layout

| Path | What belongs there |
|---|---|
| `cmd/atak-dl/` | `main.go` and one file per cobra command in `commands/` |
| `internal/xmlparser/` | The source file parser and `MapSource` |
| `internal/tilecalc/` | Web Mercator tile math |
| `internal/download/` | The download engine |
| `internal/mbtiles/` | The MBTiles writer (becomes `internal/output/` in Epic 6) |
| `internal/policy/`, `internal/jobs/`, `internal/server/`, `internal/testtiles/` | Planned packages. See `spec.md` § Architecture & stack |
| `web/` | The embedded web UI. Vendor every asset here. Load nothing from a CDN |
| `mapsources/` | Source files (replaced by the embedded snapshot in Epic 5) |
| `docs/specs/`, `docs/adr/` | The spec and the decision records |

## Commands

- Build: `go build -o atak-dl ./cmd/atak-dl`. After Epic 0, build with `CGO_ENABLED=0`.
- Test: `go test -race ./...`
- Vet: `go vet ./...`
- Format: `gofmt -l .` must print nothing. Use `gofmt -w <file>` to fix a file.
- Lint (after Epic 0): `go tool golangci-lint run`
- Smoke (after Epic 0): `./scripts/smoke.sh`
- Run the web UI: `./atak-dl serve`, then open http://localhost:5000.
- Regenerate the spec site after a spec edit: `python3 docs/specs/render-spec.py`

Note: on macOS 27 the CGO link step of `mattn/go-sqlite3` fails. Until Epic 0 replaces the driver, `go build` and `go test` for packages that import `internal/mbtiles` can fail on this machine. Packages `tilecalc` and `xmlparser` test without CGO.

## Conventions

- Go standard layout and naming. Keep the existing package boundaries.
- Wrap errors with `%w`. Error strings are lower case.
- Log with `log/slog`.
- Tests are table-driven. No test calls a real tile server or Nominatim. Use the test tile server (`internal/testtiles`, Epic 0).
- Commit subjects are imperative and in sentence case, with no prefix (for example "Add context.Background() rather than nil").
- Write prose and code comments in STE. See `.claude/rules/ste.md`.

## Branches

- `branch_model: dev-and-live`. Batches merge into `develop`. A release merges `develop` into `main` and pushes a `v*` tag.
- Epic 0 creates `develop`. Until then, `main` is the only branch.

## Filing gate

- **A finding earns a tracker issue in five cases, and never otherwise.** Behavior. A user-visible output. A guard that guards nothing. A blocked epic. A question the maintainer must rule. One carve-out: anything under `docs/specs/` that describes the wrong product earns a spec-update issue, because the spec decides what gets built.

  **Every other finding is repaired by the change set that found it, and no issue is filed.** A falsified sentence, a moved citation, a stale count, a missing term and prose drift each reach the repair. A repair that turns out to touch behavior stops and is filed as a behavior finding.

  Where the repair goes:
  - Found while building an issue → that issue's own PR, limited to files it already touches.
  - Found at review of the batch integration branch → a documentation commit on that branch, before the CI-trigger commit.
  - Found with nothing open → the next batch, as a documentation commit on its integration branch.

- A changelog or spec-changelog entry records a **decision**, made at a batch gate. No rule, hook, CI check, or test may require a changelog row, a spec edit, or a docs-accuracy pass **per change set or per batch member**. A batch that shipped as planned earns no line. A test that asserts per-member bookkeeping agreement re-creates the churn the filing gate exists to prevent, and fails review here.
