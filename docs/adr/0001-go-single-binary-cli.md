# 0001 — Go, cobra and one binary with an embedded web UI

Date: 2026-11-21
Status: accepted

## Context

Reconstructed on 2026-11-21 from the code during project-inherit; the original rationale was not recorded.

The project serves solo operators on Linux, macOS and Windows. The README promises
"Single binary deployment". The code is in Go (`go.mod`, `go 1.21`). The CLI uses
`github.com/spf13/cobra` v1.8.0 (`cmd/atak-dl/main.go`). `web/embed.go` embeds
`index.html` and `static/` with `embed.FS`.

## Decision

The project is one Go binary, `atak-dl`. Cobra defines the subcommands
`list-sources`, `estimate`, `tile-info`, `validate-source`, `download` and `serve`. The
`serve` command delivers the web UI from files that are embedded in the binary.

## Consequences

- The web UI needs no separate install step.
- The map source files are not embedded. They must sit next to the binary in
  `mapsources/`. See ADR 0004.
- The embedded page loads its libraries from public hosts, so the binary does not work
  on a disconnected machine. The planned `offline-web-ui` feature revisits this.
- The SQLite driver needs CGO, which breaks the cross-compile promise. See ADR 0002.
