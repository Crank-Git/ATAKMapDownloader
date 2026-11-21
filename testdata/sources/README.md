# Fixture source files

Tests read these source files. Each URL except the one in `serverparts.xml` starts with `http://tiles.test`. A test replaces that prefix with the URL of the test tile server in `internal/testtiles`.

| File | Map source |
|---|---|
| `xyz.xml` | A `{$x}/{$y}/{$z}` map source. |
| `quadkey.xml` | A `{$q}` quadkey map source. |
| `serverparts.xml` | A map source with the server parts `a,b,c` in the host. A test sends each host to the test tile server. |
| `tms.xml` | A map source with `invertYCoordinate` true, so `{$y}` is the TMS row. |
| `wms.xml` | A `customWmsMapSource` with version 1.1.1 and CRS `EPSG:3857`. |
