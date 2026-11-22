# Project layout

This page lists the directories of the repository. For the build and test commands, see the Development section of the [README](../README.md#development).

```
ATAK-Map-Downloader/
├── cmd/atak-dl/          # main.go, and one file per cobra command in commands/
├── internal/
│   ├── areafile/         # GeoJSON, KML and KMZ area files
│   ├── datapackage/      # ATAK data package zip and manifest
│   ├── download/         # Download engine
│   ├── jobs/             # Job queue and job store of the team server
│   ├── mbtiles/          # MBTiles writer
│   ├── output/           # Output format registry
│   │   ├── gpkg/         # GeoPackage writer
│   │   ├── osmdroid/     # OSMDroid writer
│   │   └── sqlitequeue/  # Writer core that the three output formats share
│   ├── policy/           # Embedded source policy table and its host match
│   ├── snapshot/         # Embedded ATAK-Maps source files
│   ├── sourcesync/       # sources sync from ATAK-Maps releases
│   ├── testtiles/        # httptest tile server for tests
│   ├── tilecalc/         # Tile coordinate calculations
│   └── xmlparser/        # XML map source parser
├── web/                  # Embedded web UI: index.html, jobs.html, signin.html and static/
├── scripts/smoke.sh      # Smoke test
├── docs/                 # User documentation, specs and decision records
└── go.mod
```
