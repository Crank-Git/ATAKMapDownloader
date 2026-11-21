# ATAK Map Downloader

> Download map tiles and create MBTiles databases for ATAK (Android Team Awareness Kit).

A Go implementation of the ATAK Tile Downloader. It downloads map tiles from many map sources and creates MBTiles databases that ATAK can read.

**Cross-platform**: Linux, macOS, Windows. Single binary deployment.

## About

ATAK Map Downloader parses map sources from MOBAC XML files, computes tile coordinates in Web Mercator projection, and downloads tiles concurrently to produce MBTiles databases. Use the CLI for scripting or the web interface for interactive area selection.

**Features:**
- XML map source parser: discovers and parses the 30 map sources in the MOBAC XML files of `mapsources/`
- Tile calculations: Web Mercator projection, coordinate transformations
- CLI: `list-sources`, `estimate`, `tile-info`, `validate-source`, `download`
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
3. Extract the archive into a new directory. The archive contains the `atak-dl` binary and the `mapsources/` directory.
4. Run `./atak-dl` from that directory. The binary reads its map sources from `mapsources/` in the current directory.

Each release also has a `checksums.txt` file. Use it to verify the archive.

### Docker image

The image is `ghcr.io/crank-git/atak-map-downloader`. It contains the binary and the `mapsources/` directory. Replace `<tag>` with a release tag, for example `v1.0.0`.

```bash
# Start the web interface on port 5000
docker run -p 5000:5000 ghcr.io/crank-git/atak-map-downloader:<tag>

# Show the version
docker run ghcr.io/crank-git/atak-map-downloader:<tag> --version
```

The image runs `atak-dl serve --addr 0.0.0.0:5000` by default. Arguments after the image name replace `serve --addr 0.0.0.0:5000`.

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

# Estimate download for an area
./atak-dl estimate --source="Google - Satellite Only" --bbox=40.68,-74.05,40.88,-73.91

# Get tile info for coordinates
./atak-dl tile-info --lat=40.7589 --lon=-73.9851 --zoom=15

# Validate a map source
./atak-dl validate-source --source="Google - Satellite Only"

# Download tiles
./atak-dl download --source="Google - Satellite Only" --bbox=40.7,-74.1,40.8,-74.0 --min-zoom=10 --max-zoom=12
```

### Options

| Command | Flag | Default | Description |
|---------|------|---------|-------------|
| (global) | `--mapsources-dir` | `mapsources` | Directory containing XML map source files |
| `serve` | `--addr` | `localhost:5000` | Address to listen on |
| `download` | `--source` | - | Map source name (required) |
| `download` | `--bbox` | - | Bounding box: min_lat,min_lon,max_lat,max_lon (required) |
| `download` | `--min-zoom` | 1 | Minimum zoom level |
| `download` | `--max-zoom` | 15 | Maximum zoom level |
| `download` | `--output` | `downloads/<name>.sqlite` | Output file path |
| `download` | `--concurrent` | 5 | Concurrent downloads |

## Map Sources

XML files in `mapsources/` are from [ATAK-Maps](https://github.com/joshuafuller/ATAK-Maps) by Joshua Fuller (MOBAC format). Place or symlink map source XMLs in the `mapsources` directory.

## Project Structure

```
ATAKMapDownloader/
├── cmd/atak-dl/          # CLI entry and commands
├── internal/
│   ├── xmlparser/        # XML map source parser
│   ├── tilecalc/        # Tile coordinate calculations
│   ├── download/        # Download engine
│   └── mbtiles/         # MBTiles database
├── web/                 # Embedded web UI
│   ├── index.html
│   └── static/
├── mapsources/          # XML map source definitions
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
