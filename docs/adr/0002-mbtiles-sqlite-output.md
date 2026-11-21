# 0002 — SQLite output file with the four-column TMS schema

Date: 2026-11-21
Status: accepted

## Context

Reconstructed on 2026-11-21 from the code during project-inherit; the original rationale was not recorded.

ATAK imports tiles from SQLite files. `internal/mbtiles/mbtiles.go` creates a `metadata`
table and a `tiles` table with `zoom_level`, `tile_column`, `tile_row` and `tile_data`.
The engine writes TMS rows (`tile_row = 2^z − 1 − y`). The driver is
`github.com/mattn/go-sqlite3` v1.14.24, which needs CGO. The output file name ends in
`.sqlite`.

ATAK detects the type of a SQLite imagery file by the columns of its `tiles` table, not
by its extension. A `tiles` table with exactly the four columns above is the "momap" type
in ATAK-CIV, which reads TMS rows
([TilesetInfo.java](https://github.com/deptofdefense/AndroidTacticalAssaultKit-CIV/blob/main/takkernel/engine/src/main/java/com/atakmap/android/maps/tilesets/TilesetInfo.java),
ATAK-CIV 4.6.0.5). This was read from archived source code, not tested on a device.

## Decision

The project writes one SQLite output file per job, with the MBTiles-style four-column
TMS schema. The metadata holds the keys `name`, `type`, `version`, `description`,
`format`, `bounds`, `minzoom` and `maxzoom`.

## Consequences

- The `.sqlite` extension carries no meaning to ATAK. The schema decides the type.
- The output file is not fully MBTiles 1.3 compliant. It has no `center` key, and ATAK
  files it as momap, not as MBTiles.
- CGO makes plain `GOOS`/`GOARCH` cross-compiles fail. On macOS 27 the link step fails
  today. The planned foundation work replaces the driver with a pure-Go one.
- Other output formats (OSMDroid, GeoPackage, a data package) need new writers. The
  planned `output-formats` feature covers them.
