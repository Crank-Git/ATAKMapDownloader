# 0003 — Local server with no authentication

Date: 2026-11-21
Status: superseded by 0006 for the CORS and websocket parts (#77, #78). The no-authentication part stays accepted.

## Context

Reconstructed on 2026-11-21 from the code during project-inherit; the original rationale was not recorded.

The `serve` command binds to `localhost:5000` by default (`cmd/atak-dl/commands/serve.go:20`).
The server has no authentication. It sends `Access-Control-Allow-Origin: *` on every
response, and the websocket upgrader accepts any origin
(`cmd/atak-dl/commands/web.go:44`, `:54`). The code comment says the CORS headers
"avoids 403 on cross-origin requests". The server runs one job at a time, held in memory.

## Decision

The web UI is a single-user tool on the local machine. It trusts every request that
reaches the server.

## Consequences

- Any web page that the user visits can call the API and start a job that
  writes files to disk.
- `--addr 0.0.0.0:5000` exposes the server to the whole network with no control.
- The `security-hardening` feature replaces the CORS and origin parts of this
  decision. The accepted ADR `0006-sse-job-api.md` replaces the websocket job channel.
  The `team-server` feature and ADR `0009-shared-token.md` add a shared token for a non-loopback bind.
