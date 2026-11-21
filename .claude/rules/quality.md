---
paths:
  - "**/*.go"
  - "web/**/*.{js,html,css}"
---

# Quality rules: reviewed per rule, pass or fail

- No error is discarded. `x, _ := f()` on an error return fails review unless a comment states why the error cannot occur.
- Wrap errors with `%w` when you add context.
- No try/catch-style recover that only logs and continues.
- No defensive nil checks against states the types already exclude.
- No abstraction with a single caller. Inline it until a second caller exists. The `output.Writer` interface is the planned exception (three output formats).
  Exception: a function extracted to meet the lint complexity limits in `.golangci.yml` is allowed with a single caller.
- No configuration for a value that nothing varies.
- No dead code and no commented-out code.
- HTTP handlers encode typed response structs, not `map[string]interface{}`.
- Every HTTP response sets `Content-Type`. Errors use the shape in `docs/specs/features/05-security-hardening.md`.
- The download engine never calls `time.Sleep`. Every wait selects on the job context.
- A value that two goroutines touch uses `sync/atomic`, a mutex, or one owner goroutine with a channel.
- Every outbound HTTP request uses `http.NewRequestWithContext`.
- The web UI inserts server and user strings with `textContent`, never `innerHTML`.
- The web UI loads no asset from a CDN.
- Tests are table-driven where more than two cases share one shape.
- A comment states what the code cannot. Delete a comment that restates the code.
