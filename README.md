# ATAK Map Downloader

> Download map tiles and create MBTiles databases for ATAK (Android Team Awareness Kit).

A Go implementation of the ATAK Tile Downloader. It downloads map tiles from many map sources and creates MBTiles databases that ATAK can read.

**Cross-platform**: Linux, macOS, Windows. Single binary deployment.

## About

ATAK Map Downloader parses map sources from MOBAC XML files, computes tile coordinates in Web Mercator projection, and downloads tiles concurrently to produce MBTiles databases. Use the CLI for scripting or the web interface for interactive area selection.

**Features:**
- Source catalog: the binary embeds the 53 source files of ATAK-Maps v2.3.1. A user source directory adds map sources or replaces them by name
- Tile calculations: Web Mercator projection, coordinate transformations
- CLI: `list-sources`, `sources sync`, `estimate`, `tile-info`, `validate-source`, `download`
- Web interface: interactive map, area selection, real-time download progress
- Download engine: concurrent downloads, retries, MBTiles output

## Requirements

- [Go](https://go.dev/) 1.26 or later, to build from source. A C compiler is not necessary.

Platform: macOS, Linux, or Windows.

## Install

You can install the project in two ways: a release archive or the Docker image.

### Release archive

1. Open the [GitHub Releases](https://github.com/Crank-Git/ATAKMapDownloader/releases) page.
2. Download the archive for your operating system and architecture. The Windows archive is a `.zip` file. The other archives are `.tar.gz` files.
3. Extract the archive into a new directory. The archive contains the `atak-dl` binary, `README.md` and `LICENSE`.
4. Run `./atak-dl` from that directory. The binary carries its map sources, so it needs no other file.

Each release also has a `checksums.txt` file. Use it to verify the archive.

### Docker image

The image is `ghcr.io/crank-git/atak-map-downloader`. It contains the binary. Replace `<tag>` with a release tag, for example `v1.0.0`.

```bash
# Start the web interface on port 5000
docker run -p 5000:5000 ghcr.io/crank-git/atak-map-downloader:<tag>

# Show the version
docker run ghcr.io/crank-git/atak-map-downloader:<tag> --version
```

The image runs `atak-dl serve --addr 0.0.0.0:5000` by default. Arguments after the image name replace `serve --addr 0.0.0.0:5000`. The address `0.0.0.0` is not loopback, so the server prints a warning that it has no authentication.

## Build

```bash
CGO_ENABLED=0 go build -o atak-dl ./cmd/atak-dl
```

The SQLite driver is pure Go, so the build needs no C toolchain.

### Cross-Platform Build

```bash
# Linux (amd64)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o atak-dl-linux-amd64 ./cmd/atak-dl

# Linux (arm64)
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o atak-dl-linux-arm64 ./cmd/atak-dl

# macOS (Intel)
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o atak-dl-darwin-amd64 ./cmd/atak-dl

# macOS (Apple Silicon)
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o atak-dl-darwin-arm64 ./cmd/atak-dl

# Windows
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o atak-dl.exe ./cmd/atak-dl
```

### Version

```bash
./atak-dl --version
```

A release binary shows its release version, commit and build date. A local build shows `atak-dl version dev (commit none, built unknown)`.

## Run

### Web Interface

```bash
./atak-dl serve
```

Then open http://localhost:5000 in your browser.

### CLI Commands

```bash
# List available map sources
./atak-dl list-sources

# Download the latest ATAK-Maps release into atak-maps/ in the user source directory
./atak-dl sources sync

# Estimate download for an area
./atak-dl estimate --source="Google Satellite" --bbox=40.68,-74.05,40.88,-73.91

# Get tile info for coordinates
./atak-dl tile-info --lat=40.7589 --lon=-73.9851 --zoom=15

# Validate a map source
./atak-dl validate-source --source="Google Satellite"

# Download tiles
./atak-dl download --source="Google Satellite" --bbox=40.7,-74.1,40.8,-74.0 --min-zoom=10 --max-zoom=12 --accept-terms
```

### Options

| Command | Flag | Default | Description |
|---------|------|---------|-------------|
| (global) | `--mapsources-dir` | `<user config dir>/atak-dl/mapsources` | User source directory. The project loads it after the snapshot, and it reads its subdirectories too. A missing directory is not an error. |
| (global) | `--no-snapshot` | `false` | Load only the user source directory, without the embedded snapshot. |
| `sources sync` | `--tag` | latest release | ATAK-Maps release tag to download, for example `v2.3.1`. |
| `serve` | `--addr` | `localhost:5000` | Address to listen on. If the host is not a loopback address, `serve` prints a warning, because the server has no authentication. |
| `serve` | `--geocoder-url` | `https://nominatim.openstreetmap.org` | Base URL of the Nominatim geocoder for place search. The server sends at most 1 geocode request per second, waits at most 5 s for a slot, and caches each result for 24 hours. A URL that is not an absolute `http` or `https` URL fails. |
| `serve` | `--no-geocode` | `false` | Turn off place search. `POST /api/geocode` returns 404 `not_found`, `GET /api/config` returns `{"geocode": false}`, and the web UI hides the search box. |
| `serve` | `--max-tiles` | `500000` | Tile cap of a job. `POST /api/jobs` for a job above the cap returns 400 `too_many_tiles`, and the job sends no request. |
| `download` | `--source` | - | Map source name (required) |
| `download` | `--bbox` | - | Bounding box: min_lat,min_lon,max_lat,max_lon (required) |
| `download` | `--min-zoom` | 1 | Minimum zoom level. It must not be below the map source's minimum zoom level, or the command fails with `zoom_out_of_range`. |
| `download` | `--max-zoom` | 15 | Maximum zoom level. It must not be above the map source's maximum zoom level, or the command fails with `zoom_out_of_range`. |
| `download` | `--format` | `mbtiles` | Output format: `mbtiles`, `osmdroid` or `gpkg`. The extension of the default output file is `.mbtiles`, `.sqlite` or `.gpkg`. An unknown value fails with `invalid_output_format` and lists the valid values. A valid format without a writer in this build fails with `not_implemented`. A `gpkg` output file is a GeoPackage 1.3 raster in EPSG:3857, with one tile table named from the map source. An `osmdroid` output file is an OSMDroid tile cache in EPSG:3857 that ATAK reads, with the map source name in `provider`. It accepts zoom levels up to 27 and refuses a higher zoom level with `zoom_out_of_range`. `POST /api/jobs` accepts the same values in `output_format`, and returns 400 with the same error codes. |
| `download` | `--output` | `downloads/<name>.mbtiles` | Output file path. If the file exists and the command has neither `--resume` nor `--overwrite`, the command exits with code 3 and the file stays unchanged. A cancelled or failed job keeps the file and every tile it stored. |
| `download` | `--resume` | `false` | Continue the job into the existing output file. The job skips each tile that the file holds and counts it as `skipped`. If the file belongs to another map source, or if its output format is not the `--format` value, the command exits with code 3 and prints `output file is <format>` for a format mismatch. |
| `download` | `--overwrite` | `false` | Delete the existing output file before the job starts. You cannot use it together with `--resume`. |
| `download` | `--force` | `false` | With `--resume`, accept an output file that names no map source, for example a file from v0.1. |
| `download` | `--concurrent` | 5 | Concurrent downloads, from 1 to 32. A value outside this range fails with `invalid_concurrency`. |
| `download` | `--insecure` | `false` | Do not verify the TLS certificates of the tile server. The command prints a warning to stderr when this flag is set. |
| `download` | `--rate-limit` | `0` | Requests per second to the tile server. A value only lowers the policy rate of the map source, and `0` keeps it. A value above the policy rate gives the policy rate, with a note on stderr. A negative value fails with `invalid_rate_limit`. |
| `download` | `--accept-terms` | `false` | Accept the terms of use of a `restricted` map source. Without this flag, the command prints the reason and the reference URL for a restricted map source, and it exits with code 2. With this flag, it prints a warning to stderr and starts the job. |
| `download` | `--max-tiles` | `500000` | Tile cap of the job. If the job has more tiles, the command prints `too_many_tiles` and exits with code 4 before it sends a request. |
| `download` | `--data-package` | `false` | Wrap the output file in an ATAK data package, `<package name>.zip`, beside the output file. Import it on the device through ATAK's Import Manager. The zip holds `MANIFEST/manifest.xml` (Mission Package v2, with a new UUID v4 as `uid` and `onReceiveImport` set to `true`) and the output file. The command removes the unzipped output file after the zip is complete. If the zip step fails, the command prints `package_failed` and keeps the output file. An existing zip follows the `--resume` and `--overwrite` rules of the output file. With `--resume`, the job resumes the output file and then rebuilds the zip. `POST /api/jobs` accepts `data_package`, and the job record then names the zip in `output_file`. A failed zip step ends the job `failed` with `package_failed`. `GET /api/files` lists the zip with `format` set to `data_package`. |
| `download` | `--include-source` | `false` | With `--data-package`, add the source file of the map source to the zip. `POST /api/jobs` accepts `include_source`. |
| `download` | `--package-name` | - | With `--data-package`, the package name (required). It holds 1 to 64 characters of `A-Z`, `a-z`, `0-9`, space, `_` and `-`. Each space becomes `_` in the file name. Another name fails with `invalid_package_name`. `POST /api/jobs` accepts `package_name`, and returns 400 `invalid_package_name` for a name that is not valid. |

### Progress and exit codes

While a job runs, `download` prints a progress line. The percentage counts downloaded, skipped and failed tiles:

```
37.4% (44,880/120,000) - 12.1 tiles/s - 1,200 skipped - 14 failed - ETA 1h43m
```

When the job ends, it prints one summary line:

```
Done: 118,640 downloaded, 1,200 skipped, 160 failed in 2h41m -> downloads/OpenTopoMap_Mt_Hood_z8-15.mbtiles
```

If you press Ctrl-C, the engine stops its requests within 500 ms and commits the stored tiles. The command prints the stored count and the hint `Run again with --resume.`

`download` exits with one of these codes:

| Code | Meaning |
|------|---------|
| 0 | The job succeeded. |
| 1 | An error that has no other code. |
| 2 | The map source is restricted, and the command has no `--accept-terms`. |
| 3 | The output file exists, it belongs to another map source, it has another output format, or it is not an output file. With `--resume` and no `--force`, a file with no map source name also exits with 3. |
| 4 | The job has more tiles than `--max-tiles`. |
| 5 | The failure rate of the job is above the failure tolerance. |
| 130 | Ctrl-C or SIGTERM cancelled the job. |

## Map Sources

The binary embeds a snapshot of the source files of [ATAK-Maps](https://github.com/joshuafuller/ATAK-Maps) v2.3.1, by Joshua Fuller (MIT License). The files are MOBAC XML, in `internal/snapshot/maps/`. `internal/snapshot/NOTICE.md` credits ATAK-Maps, and `internal/snapshot/VERSION` holds the tag and the commit.

A source file can set these elements for the tile URL:

- `serverParts`: a list of server parts, separated by white space or commas. A tile always uses the part `(x + y) mod N`, so it always goes to the same host.
- `invertYCoordinate`: with the exact value `true`, the project sends the TMS row `(2^z - 1) - y` as `{$y}`.
- `tileUpdate`: a missing element gives `None`.

The project loads the snapshot first. Then it loads the user source directory, which `--mapsources-dir` names. The default user source directory is `<user config dir>/atak-dl/mapsources`, for example:

- Linux: `~/.config/atak-dl/mapsources`
- macOS: `~/Library/Application Support/atak-dl/mapsources`
- Windows: `%AppData%\atak-dl\mapsources`

`atak-dl sources sync` downloads the latest ATAK-Maps release from GitHub. With `--tag`, it downloads that release. The command writes the files to `<user source directory>/atak-maps/`:

- each `.xml` file under `maps/` in the release, in the upstream layout, for example `atak-maps/maps/base/osm/osm.xml`
- `VERSION`, with the tag and the commit of the release

The command replaces only `atak-maps/`, so your own source files beside it stay. It also removes the hidden `.atak-maps-new-*` and `.atak-maps-old-*` directories that an interrupted sync can leave. It prints `<tag>: N added, N changed, N removed`. The command keeps the existing source files and exits with code 1 in these cases:

- a request fails
- the archive has a path with `..`, or no `maps/` directory
- the response is larger than 64 MiB, the files in the archive are larger than 256 MiB in total, or the archive has more than 10000 entries

The command skips symbolic links in the archive. If it cannot put the old files back after a failed swap, it keeps them in a `.atak-maps-kept-*` directory and names that directory in the error. A later sync does not remove it. A GitHub rate-limit response gives a message with the time to try again.

The project loads the map sources in three layers: the snapshot, then `atak-maps/`, then your own source files elsewhere in the user source directory. A map source of a later layer replaces the map source with the same name, so your own file always wins. `list-sources` shows the origin of each map source: `snapshot` or `user`. With `--no-snapshot`, the project loads only the user source directory.

ATAK-Maps v2.0.0 renamed every map source. `internal/snapshot/aliases.json` maps each v0.1 name to its v2 name. These old names stay until v0.6.0 at the earliest:

- `--source` with an old name uses the v2 name and prints a warning on stderr, for example `Warning: "Google - Satellite Only" is an old map source name. The new name is "Google Satellite".`
- `POST /api/jobs` with an old `source` uses the v2 name in the job record. The SSE stream of the job sends the warning as `event: log` with `level` `warning`.
- `POST /api/validate-source` and `POST /api/preview` with an old name use the v2 name in the response. The server log gets the warning.
- An old name with no v2 equivalent fails with `map source removed upstream in ATAK-Maps <version>`, for example v1.10.1 for Michelin.
- A loaded map source with the old name comes first, so a user source file with a v0.1 name keeps that name.

A source file that does not load is a skipped source file. `atak-dl list-sources --skipped` prints each one as `path  reason  detail`. `GET /api/sources/skipped` returns the same list as `[{path, reason, detail}]`, and the web UI shows "N sources skipped" under the source picker. The reasons are:

- `parse_error`: the file is not valid XML, or its root element is not `customMapSource`.
- `missing_name`: the `<name>` element is empty or missing.
- `missing_url`: the `<url>` element is empty or missing.
- `unsupported_crs`: reserved for WMS source files.
- `unknown_placeholder`: the `<url>` element holds a URL placeholder other than `{$z}`, `{$x}`, `{$y}`, `{$q}` and `{$serverpart}`. These are the URL placeholders of the [ATAK-Maps XML reference](https://github.com/joshuafuller/ATAK-Maps/blob/v2.3.1/docs/xml-reference.md#url-placeholders). The letter case must match.
- `duplicate_name`: a file in the same directory has the same name. The first file in lexical path order loads.

## Project Structure

```
ATAKMapDownloader/
├── cmd/atak-dl/          # CLI entry and commands
├── internal/
│   ├── snapshot/         # Embedded ATAK-Maps source files
│   ├── sourcesync/       # sources sync from ATAK-Maps releases
│   ├── xmlparser/        # XML map source parser
│   ├── tilecalc/        # Tile coordinate calculations
│   ├── download/        # Download engine
│   ├── output/          # Output writer interface and format registry
│   ├── datapackage/     # ATAK data package zip and manifest
│   └── mbtiles/         # MBTiles writer
├── web/                 # Embedded web UI
│   ├── index.html
│   └── static/
└── go.mod
```

## Development

```bash
go test -race ./...           # Run the tests with the race detector
go vet ./...                  # Run go vet
go tool golangci-lint run     # Run the linter
scripts/smoke.sh              # Run the smoke test
```

The `golangci-lint` version is pinned in `go.mod` as a tool dependency. If the linter binary does not link on macOS, run `CGO_ENABLED=0 go tool golangci-lint run`.

## License

This project is licensed under the MIT License. See [LICENSE](LICENSE) for details.
