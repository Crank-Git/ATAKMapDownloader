#!/bin/sh
# smoke.sh builds atak-dl, starts serve on a free port, and checks eleven responses and the bind warning.
# It also checks the terms exit code, the output file exit code, the tile cap, --no-geocode and the sources sync command.
# FR-offline-web-ui-1, -7, -10 and -13, FR-foundation-13, FR-foundation-14, FR-security-hardening-3, -9, -17 and -25,
# FR-polite-downloading-18 and -26, FR-robust-downloads-6, -26 and -27, and FR-source-sync-10, -11, -16 and -29 define the checks.
# It needs go, curl and python3. It exits 0 when every check passes.
set -eu

# go build needs the module root, so the script runs from the repository root.
cd "$(dirname "$0")/.."

CGO_ENABLED=0 go build -o atak-dl ./cmd/atak-dl

# A new home directory has no user source directory, so serve loads only the embedded snapshot.
# The build runs first, because the Go cache is in the real home directory.
smoke_home=$(mktemp -d)
export HOME="$smoke_home" XDG_CONFIG_HOME="$smoke_home/.config"

# The OS gives a free port to a socket that binds port 0.
port=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
base="http://127.0.0.1:$port"

./atak-dl serve --addr "127.0.0.1:$port" >/dev/null &
pid=$!
trap 'kill "$pid" 2>/dev/null || true; rm -rf "$smoke_home"' EXIT INT TERM

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
if not isinstance(body, list) or "Google Satellite" not in [s.get("name") for s in body]:
    sys.exit("smoke: GET /api/sources did not return the snapshot map sources")
print("smoke: GET /api/sources returned %d snapshot sources" % len(body))
'

# The snapshot holds source files that the parser does not load, so the skipped list is not empty (FR-source-sync-29).
curl -sf "$base/api/sources/skipped" | python3 -c '
import json, sys
body = json.load(sys.stdin)
if not isinstance(body, list) or not body or any(sorted(s) != ["detail", "path", "reason"] for s in body):
    sys.exit("smoke: GET /api/sources/skipped did not return [{path, reason, detail}]")
print("smoke: GET /api/sources/skipped returned %d skipped source files" % len(body))
'

# The page loads Leaflet from the binary, so the web UI works with no internet (FR-offline-web-ui-1).
status=$(curl -s -o /dev/null -w '%{http_code}' "$base/static/vendor/leaflet/leaflet.js")
if [ "$status" != "200" ]; then
	echo "smoke: GET /static/vendor/leaflet/leaflet.js returned $status, want 200" >&2
	exit 1
fi
echo "smoke: GET /static/vendor/leaflet/leaflet.js returned 200"

# No job runs yet, so the job API returns the job_not_found error shape.
curl -s "$base/api/jobs/000000000000" | python3 -c '
import json, sys
body = json.load(sys.stdin)
if body.get("error", {}).get("code") != "job_not_found":
    sys.exit("smoke: GET /api/jobs/{id} did not return job_not_found")
print("smoke: GET /api/jobs/{id} returned job_not_found")
'

# GET /api/files returns a JSON array, also when the output directory does not exist yet (FR-offline-web-ui-7).
curl -sf "$base/api/files" | python3 -c '
import json, sys
if not isinstance(json.load(sys.stdin), list):
    sys.exit("smoke: GET /api/files did not return a JSON array")
print("smoke: GET /api/files returned a JSON array")
'

# An encoded parent path returns 404 file_not_found and not the file (FR-offline-web-ui-10).
curl -s -w '\n%{http_code}' "$base/api/files/..%2Fgo.mod" | python3 -c '
import json, sys
body, status = sys.stdin.read().rsplit("\n", 1)
if status != "404" or json.loads(body).get("error", {}).get("code") != "file_not_found":
    sys.exit("smoke: GET /api/files/..%2Fgo.mod returned " + status + ", want 404 file_not_found")
print("smoke: GET /api/files/..%2Fgo.mod returned 404 file_not_found")
'

# The tile route of a name that is not an output file returns 404 file_not_found (FR-offline-web-ui-13).
curl -s -w '\n%{http_code}' "$base/api/files/missing.mbtiles/tiles/0/0/0" | python3 -c '
import json, sys
body, status = sys.stdin.read().rsplit("\n", 1)
if status != "404" or json.loads(body).get("error", {}).get("code") != "file_not_found":
    sys.exit("smoke: GET /api/files/missing.mbtiles/tiles/0/0/0 returned " + status + ", want 404 file_not_found")
print("smoke: GET /api/files/missing.mbtiles/tiles/0/0/0 returned 404 file_not_found")
'

# A browser adds Sec-Fetch-Site: cross-site to a request from another web page.
curl -s -X POST -H 'Sec-Fetch-Site: cross-site' -H 'Content-Type: application/json' -d '{}' "$base/api/jobs" | python3 -c '
import json, sys
body = json.load(sys.stdin)
if body.get("error", {}).get("code") != "cross_origin":
    sys.exit("smoke: a cross-site POST /api/jobs did not return cross_origin")
print("smoke: a cross-site POST /api/jobs returned cross_origin")
'

# The smoke test sends no geocode request, because a test never calls the real Nominatim.
curl -sf "$base/api/config" | python3 -c '
import json, sys
if json.load(sys.stdin).get("geocode") is not True:
    sys.exit("smoke: GET /api/config did not return geocode true")
print("smoke: GET /api/config returned geocode true")
'

# A body larger than 1 MiB returns 413 (FR-security-hardening-17).
status=$(head -c 2097152 /dev/zero | curl -s -o /dev/null -w '%{http_code}' --data-binary @- "$base/api/jobs")
if [ "$status" != "413" ]; then
	echo "smoke: POST /api/jobs with a 2 MiB body returned $status, want 413" >&2
	exit 1
fi
echo "smoke: POST /api/jobs with a 2 MiB body returned 413"

# A server on an address that is not loopback prints the warning of FR-security-hardening-25.
stderr_file=$(mktemp)
./atak-dl serve --addr "0.0.0.0:0" >/dev/null 2>"$stderr_file" &
open_pid=$!
trap 'kill "$pid" "$open_pid" 2>/dev/null || true; rm -f "$stderr_file"; rm -rf "$smoke_home"' EXIT INT TERM
attempt=0
until grep -q "warning: the server has no authentication" "$stderr_file"; do
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 50 ]; then
		echo "smoke: serve --addr 0.0.0.0:0 did not print the no-authentication warning within 10 s" >&2
		exit 1
	fi
	sleep 0.2
done
echo "smoke: serve --addr 0.0.0.0:0 printed the no-authentication warning"

# A restricted map source without --accept-terms exits with code 2 (FR-polite-downloading-18).
# The output directory is a regular file, so a build without the check fails before it sends a request to Google.
# The v0.1 name goes through the alias, so the check also covers the deprecation warning (FR-source-sync-16).
terms_file=$(mktemp)
trap 'kill "$pid" "$open_pid" 2>/dev/null || true; rm -f "$stderr_file" "$terms_file"; rm -rf "$smoke_home"' EXIT INT TERM
status=0
./atak-dl download --source "Google - Satellite Only" --bbox 40.7,-74.1,40.8,-74.0 --min-zoom 10 --max-zoom 10 \
	--output "$terms_file/out.sqlite" >/dev/null 2>"$stderr_file" || status=$?
if [ "$status" != "2" ] || ! grep -q "https://developers.google.com/maps/documentation/tile/policies" "$stderr_file"; then
	echo "smoke: download of a Google map source without --accept-terms exited $status, want 2 and the reference URL" >&2
	exit 1
fi
if ! grep -qF 'The new name is "Google Satellite".' "$stderr_file"; then
	echo "smoke: download with the old name \"Google - Satellite Only\" printed no deprecation warning" >&2
	exit 1
fi
echo "smoke: download of a Google map source by its old name warned and exited 2"

# An existing output file without --resume or --overwrite exits with code 3 and stays unchanged (FR-robust-downloads-6).
# The file is not SQLite, so a build without the check fails when it opens the file, before a request.
exists_file=$(mktemp)
trap 'kill "$pid" "$open_pid" 2>/dev/null || true; rm -f "$stderr_file" "$terms_file" "$exists_file"; rm -rf "$smoke_home"' EXIT INT TERM
echo "smoke notes" >"$exists_file"
status=0
./atak-dl download --source "USGS Topo" --bbox 40.7,-74.1,40.8,-74.0 --min-zoom 10 --max-zoom 10 \
	--output "$exists_file" >/dev/null 2>"$stderr_file" || status=$?
if [ "$status" != "3" ] || ! grep -q "output file exists: use --resume or --overwrite" "$stderr_file" ||
	[ "$(cat "$exists_file")" != "smoke notes" ]; then
	echo "smoke: download into an existing output file exited $status, want 3, the message and the file unchanged" >&2
	exit 1
fi
echo "smoke: download into an existing output file exited 3"

# A job above --max-tiles exits with code 4 before it sends a request (FR-robust-downloads-26).
# The output directory is a regular file, so a build without the check fails before it sends a request.
status=0
./atak-dl download --source "USGS Topo" --bbox 40.7,-74.1,40.8,-74.0 --min-zoom 10 --max-zoom 12 \
	--max-tiles 1 --output "$terms_file/out.sqlite" >/dev/null 2>"$stderr_file" || status=$?
if [ "$status" != "4" ] || ! grep -q "too_many_tiles" "$stderr_file"; then
	echo "smoke: download above --max-tiles exited $status, want 4 and too_many_tiles" >&2
	exit 1
fi
echo "smoke: download above --max-tiles exited 4"

# serve --no-geocode removes the geocode route and reports geocode false (FR-polite-downloading-26).
port2=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
base2="http://127.0.0.1:$port2"
./atak-dl serve --addr "127.0.0.1:$port2" --no-geocode --max-tiles 1 >/dev/null &
off_pid=$!
trap 'kill "$pid" "$open_pid" "$off_pid" 2>/dev/null || true; rm -f "$stderr_file" "$terms_file" "$exists_file"; rm -rf "$smoke_home"' EXIT INT TERM
attempt=0
until [ "$(curl -s -o /dev/null -w '%{http_code}' "$base2/" || true)" = "200" ]; do
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 50 ]; then
		echo "smoke: serve --no-geocode did not answer GET / within 10 s" >&2
		exit 1
	fi
	sleep 0.2
done
status=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{"query":"Berlin"}' "$base2/api/geocode")
if [ "$status" != "404" ]; then
	echo "smoke: POST /api/geocode with --no-geocode returned $status, want 404" >&2
	exit 1
fi
curl -sf "$base2/api/config" | python3 -c '
import json, sys
if json.load(sys.stdin).get("geocode") is not False:
    sys.exit("smoke: GET /api/config with --no-geocode did not return geocode false")
'
echo "smoke: serve --no-geocode returned 404 for POST /api/geocode and geocode false"

# A job API job above serve --max-tiles returns 400 too_many_tiles before it sends a request (FR-robust-downloads-27).
curl -s -X POST -H 'Content-Type: application/json' \
	-d '{"source":"USGS Topo","bounds":[-74.1,40.7,-74.0,40.8],"min_zoom":10,"max_zoom":12}' "$base2/api/jobs" | python3 -c '
import json, sys
if json.load(sys.stdin).get("error", {}).get("code") != "too_many_tiles":
    sys.exit("smoke: POST /api/jobs above serve --max-tiles did not return too_many_tiles")
'
echo "smoke: POST /api/jobs above serve --max-tiles returned too_many_tiles"

# The binary has the sources sync command with --tag (FR-source-sync-10 and -11).
# The check reads only the help text, because a smoke test never calls the real GitHub.
if ! ./atak-dl sources sync --help | grep -q -- "--tag"; then
	echo "smoke: atak-dl sources sync --help does not show --tag" >&2
	exit 1
fi
echo "smoke: atak-dl sources sync --help shows --tag"

echo "smoke: pass"
