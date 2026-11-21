---
name: test
description: Run the atak-dl test suites and choose which ones a change needs. Use before you open or update a PR, or when asked to test.
allowed-tools: Bash, Read
---

# Test ATAK Map Downloader

1. Format: `gofmt -l .` must print nothing.
2. Vet: `go vet ./...`
3. Unit and integration tests: `go test -race ./...`. Integration tests use `internal/testtiles`, and no test calls a real server.
4. Lint (after Epic 0): `go tool golangci-lint run`
5. Smoke (after Epic 0): `./scripts/smoke.sh`

Which extra suite a change needs:

| Change touches | Also run |
|---|---|
| `internal/output/` or `internal/mbtiles/` | The conformance tests for every output format: `go test -race -run Conformance ./internal/output/...` |
| `internal/download/` | `go test -race -count=3 ./internal/download/...`, because races show up intermittently |
| `cmd/atak-dl/commands/` or `internal/server/` | `./scripts/smoke.sh` |
| `web/` | Load the web UI with the `run` skill and check the browser console for errors |

Until Epic 0 replaces the CGO driver, packages that import `internal/mbtiles` can fail to link on macOS 27. If so, run `go test ./internal/tilecalc/... ./internal/xmlparser/...`, and report the link failure as an environment problem, not a test failure.
