# 0011 — OSMDroid output file with the ATAK schema and a zoom limit of 27

Date: 2026-11-21
Status: accepted

## Context

`--format osmdroid` writes an SQLite file that ATAK reads as an OSMDroid tile cache
(FR-output-formats-11 to 14). The spec does not hold the `ATAK_metadata` schema. This
record takes the schema from the ATAK-CIV source, archived 4.6.0.5, in
`takkernel/engine/src/main/java/com/atakmap/`:

- [OSMDroidTileContainer.java](https://github.com/deptofdefense/AndroidTacticalAssaultKit-CIV/blob/main/takkernel/engine/src/main/java/com/atakmap/map/layer/raster/osm/OSMDroidTileContainer.java)
- [OSMUtils.java](https://github.com/deptofdefense/AndroidTacticalAssaultKit-CIV/blob/main/takkernel/engine/src/main/java/com/atakmap/map/layer/raster/osm/OSMUtils.java)
- [OSMDroidInfo.java](https://github.com/deptofdefense/AndroidTacticalAssaultKit-CIV/blob/main/takkernel/engine/src/main/java/com/atakmap/map/layer/raster/osm/OSMDroidInfo.java)
- [TilesetInfo.java](https://github.com/deptofdefense/AndroidTacticalAssaultKit-CIV/blob/main/takkernel/engine/src/main/java/com/atakmap/android/maps/tilesets/TilesetInfo.java)

The source was read on 2026-11-21. It was not tested on a device.

**Detection.** `OSMUtils.isOSMDroidSQLite` accepts a file when the column set of `tiles`
equals `key, provider, tile`. `TilesetInfo.parseSQLiteDb` tests this rule first. It
tests no other table, so ATAK ignores the `atakdl_` tables. `parseSQLiteDb` returns no
dataset for a file without tiles.

**Tables.** `OSMDroidTileContainer.createTables` creates these tables, with
`atakMetadataAlways` set to true for a tile cache that ATAK writes:

```sql
CREATE TABLE tiles (key INTEGER PRIMARY KEY, provider TEXT, tile BLOB)
CREATE TABLE ATAK_catalog (key INTEGER PRIMARY KEY, access INTEGER, expiration INTEGER, size INTEGER)
CREATE TABLE ATAK_metadata (key TEXT, value TEXT)
INSERT INTO ATAK_metadata (key, value) VALUES ('srid', 3857)
```

`ATAK_metadata` holds one row, `srid`. `TilesetInfo` and `OSMDroidInfo` read it with
`SELECT value FROM ATAK_metadata WHERE key = 'srid'` and `Integer.parseInt`. A missing
row gives 3857. A value of 4326 changes the grid to two tiles at zoom level 0. The
container reads the expiration of a tile from `ATAK_catalog` only when both ATAK tables
exist. A tile without an `ATAK_catalog` row has no expiration (`getTileExpiration`
returns -1).

**Key.** `OSMUtils.getOSMDroidSQLiteIndex` computes the key with the XYZ row:

```java
return (((level << level) + tilex) << level) + tiley;
```

The highest key at zoom level z is `(z + 1) · 4^z − 1`. That value fits in int64 up to
zoom level 29, and it overflows at zoom level 30. The read side has a lower limit.
`getOSMDroidSQLiteMinIndex` returns -1 above zoom level 28, so
`getOSMDroidSQLiteMaxIndex(28)` is -2. `getOSMDroidSQLiteZoomLevel` therefore returns -1
for every key of zoom level 28 or higher. `TilesetInfo` and `OSMDroidInfo` read the zoom
levels of a file with this function, so they cannot read a tile above zoom level 27.

The project limits a map source to zoom level 25 (`internal/xmlparser/xmlparser.go`), so
no job reaches zoom level 28 today.

## Decision

The OSMDroid writer creates the three tables and the `srid` row exactly as
`createTables` does. It stores the map source name in `provider` and the XYZ key in
`key`. It adds `atakdl_metadata` and `atakdl_failed_tiles` for `--resume` and the retry
pass. It refuses a zoom level above 27 with `zoom_out_of_range`, because ATAK cannot
read a tile above zoom level 27. On `Close`, it sets `journal_mode=DELETE`, so the job
leaves one file.

## Consequences

- ATAK reads the file the same as a tile cache that ATAK wrote, with the type
  `osmdroid.atak`.
- The tiles never expire in ATAK, because `ATAK_catalog` stays empty.
- The zoom limit is 27, not 20 as the spec edge case supposes. The edge case states its
  limit as a condition to check, and the source shows that the condition is false.
- The writer refuses an existing file with an `srid` other than 3857 or 900913. An
  XYZ key in an EPSG:4326 grid would show at the wrong place.
- A later raise of the map source zoom limit above 27 makes the writer refuse those
  jobs, and the job API then returns the writer error.
