# 0006 — Replace the websocket with a job API and SSE

Date: 2026-11-21
Status: accepted

## Context

The web UI sends commands and receives progress over one websocket (`cmd/atak-dl/commands/web.go:389-415`). Engine goroutines write to that connection with no lock, and `gorilla/websocket` does not allow concurrent writers. `gorilla/websocket` has had no release since v1.5.3 in June 2024 (https://pkg.go.dev/github.com/gorilla/websocket?tab=versions). Progress flows only from the server to the browser. The team server needs job ids and jobs that a client can address by URL.

## Decision

Commands become HTTP requests on `/api/jobs`. Progress becomes an SSE stream at `/api/jobs/{id}/events`, sent by one goroutine per job. The project removes `gorilla/websocket`.

## Consequences

- `http.CrossOriginProtection` protects every command route in the same way.
- `EventSource` reconnects automatically, and the first event after a reconnect is the job state.
- The server has no two-way channel. If a later feature needs one, `coder/websocket` is the candidate.
- This supersedes the websocket part of ADR 0003.
