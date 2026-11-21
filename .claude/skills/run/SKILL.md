---
name: run
description: Start the atak-dl web UI or a CLI job locally for a manual check. Use when asked to run, launch, serve or screenshot the project.
argument-hint: "[port]"
allowed-tools: Bash, Read
---

# Run ATAK Map Downloader

1. Build the binary: `go build -o atak-dl ./cmd/atak-dl`. After Epic 0, prefix the command with `CGO_ENABLED=0`.
2. If the build fails at the CGO link step, the machine has the macOS 27 linker problem. Use the existing `./atak-dl` binary and say so in your report.
3. Start the server on the port you were given, or on 5000: `./atak-dl serve --addr 127.0.0.1:<port>`. Run it in the background.
4. Check that the server is up: `curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:<port>/` returns `200`.
5. Check that the source catalog loaded: `curl -s http://127.0.0.1:<port>/api/sources` returns a non-empty JSON array.
6. For a small CLI job, use the demo area. It is about 60 tiles on an `open` or `conditional` map source:
   `./atak-dl download --source "OpenTopoMap" --bbox 45.30,-121.80,45.45,-121.60 --min-zoom 8 --max-zoom 12`
7. Stop the server when you finish.

Do not start a job on a `restricted` map source (Google, Bing, Esri, OSM tile servers) for a check. Use the test tile server in tests instead of real tile servers.
