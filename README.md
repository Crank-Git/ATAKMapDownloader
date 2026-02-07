# ATAK Map Downloader

> Download map tiles and create MBTiles databases for ATAK (Android Team Awareness Kit).

A Go implementation of the ATAK Tile Downloader with 1:1 functionality matching the Python version. Downloads map tiles from various sources and creates MBTiles databases compatible with ATAK.

**Cross-platform**: Linux, macOS, Windows. Single binary deployment.

## About

ATAK Map Downloader parses map sources from MOBAC XML files, computes tile coordinates in Web Mercator projection, and downloads tiles concurrently to produce MBTiles databases. Use the CLI for scripting or the web interface for interactive area selection.

**Features:**
- XML map source parser: discovers and parses 30+ map sources from MOBAC XML files
- Tile calculations: Web Mercator projection, coordinate transformations
- CLI: `list-sources`, `estimate`, `tile-info`, `validate-source`, `download`
- Web interface: interactive map, area selection, real-time download progress
- Download engine: concurrent downloads, retries, MBTiles output

## Requirements

- [Go](https://go.dev/) 1.21 or later
- CGO (for SQLite): `go-sqlite3` requires CGO. On Windows, a C compiler (e.g., TDM-GCC) may be needed.

Platform: macOS, Linux, or Windows.

## Build

```bash
go build -o atak-dl ./cmd/atak-dl
```

### Cross-Platform Build

```bash
# Linux (amd64)
GOOS=linux GOARCH=amd64 go build -o atak-dl-linux-amd64 ./cmd/atak-dl

# macOS (Intel)
GOOS=darwin GOARCH=amd64 go build -o atak-dl-darwin-amd64 ./cmd/atak-dl

# macOS (Apple Silicon)
GOOS=darwin GOARCH=arm64 go build -o atak-dl-darwin-arm64 ./cmd/atak-dl

# Windows
GOOS=windows GOARCH=amd64 go build -o atak-dl.exe ./cmd/atak-dl
```

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
| `download` | `--output` | - | Output file path |
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
go test ./...    # Run tests
go vet ./...     # Run go vet
```

## License

This project is licensed under the MIT License. See [LICENSE](LICENSE) for details.
