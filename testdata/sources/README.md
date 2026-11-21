# Fixture source files

Tests read these source files. Each URL starts with `http://tiles.test`. A test replaces that prefix with the URL of the test tile server in `internal/testtiles`.

| File | Map source |
|---|---|
| `xyz.xml` | A `{$x}/{$y}/{$z}` map source. |
| `quadkey.xml` | A `{$q}` quadkey map source. |
| `wms.xml` | A `customWmsMapSource`. The parser does not load WMS map sources yet. |
