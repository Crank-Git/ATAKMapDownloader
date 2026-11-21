#!/bin/sh
# smoke.sh builds atak-dl, starts serve on a free port and checks two endpoints.
# FR-foundation-13 and FR-foundation-14 define the checks.
# It needs go, curl and python3. It exits 0 when every check passes.
set -eu

# serve reads ./mapsources, so the script runs from the repository root.
cd "$(dirname "$0")/.."

CGO_ENABLED=0 go build -o atak-dl ./cmd/atak-dl

# The OS gives a free port to a socket that binds port 0.
port=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
base="http://127.0.0.1:$port"

./atak-dl serve --addr "127.0.0.1:$port" >/dev/null &
pid=$!
trap 'kill "$pid" 2>/dev/null || true' EXIT INT TERM

# 50 attempts at 0.2 s give the server 10 s to start.
attempt=0
until [ "$(curl -s -o /dev/null -w '%{http_code}' "$base/" || true)" = "200" ]; do
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 50 ]; then
		echo "smoke: GET / did not return 200 within 10 s" >&2
		exit 1
	fi
	if ! kill -0 "$pid" 2>/dev/null; then
		echo "smoke: serve stopped before it answered GET /" >&2
		exit 1
	fi
	sleep 0.2
done

curl -sf "$base/api/sources" | python3 -c '
import json, sys
body = json.load(sys.stdin)
if not isinstance(body, list) or not body:
    sys.exit("smoke: GET /api/sources did not return a non-empty JSON array")
print("smoke: GET /api/sources returned %d sources" % len(body))
'

echo "smoke: pass"
