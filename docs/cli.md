# CLI reference

This page lists the flags of `atak-dl`, the progress output of `download` and its exit codes. For the map sources and the user source directory, see [sources.md](sources.md). For a team server in Docker, see [server.md](server.md).

## Options

| Command | Flag | Default | Description |
|---------|------|---------|-------------|
| (global) | `--mapsources-dir` | `<user config dir>/atak-dl/mapsources` | User source directory. The project loads it after the snapshot, and it reads its subdirectories too. A missing directory is not an error. |
| (global) | `--no-snapshot` | `false` | Load only the user source directory, without the embedded snapshot. |
| (global) | `--data-dir` | `ATAKDL_DATA_DIR`, else `./downloads` | Directory of the output files and of `jobs.jsonl`. `download` writes there when the command has no `--output`. If `serve` cannot write to the directory, it exits with code 1 and the message `data directory is not writable: <dir>`. |
| `sources sync` | `--tag` | latest release | ATAK-Maps release tag to download, for example `v2.3.1`. |
| `serve` | `--addr` | `localhost:5000` | Address to listen on. If the host is not a loopback address, every route needs the token, apart from `GET /healthz` and the sign-in page. |
| `serve` | `--geocoder-url` | `https://nominatim.openstreetmap.org` | Base URL of the Nominatim geocoder for place search. The server sends at most 1 geocode request per second, waits at most 5 s for a slot, and caches each result for 24 hours. A URL that is not an absolute `http` or `https` URL fails. |
| `serve` | `--no-geocode` | `false` | Turn off place search. `POST /api/geocode` returns 404 `not_found`, `GET /api/config` returns `{"geocode": false}`, and the web UI hides the search box. |
| `serve` | `--max-tiles` | `500000` | Tile cap of a job. `POST /api/jobs` for a job above the cap returns 400 `too_many_tiles`, and the job sends no request. |
| `serve` | (job store) | - | The server appends each state of each job to `<data dir>/jobs.jsonl`, with the job request and with no token. At start, a `running` job becomes `failed` with `reason` `server_restarted`, and a `queued` job runs. The server skips a corrupt line and logs its line number. `GET /api/jobs?page=N` returns `{"jobs": [...], "page": N, "pages": P}`, newest first, 50 per page, and 400 `invalid_query` for a page that is not a whole number of 1 or more. `POST /api/jobs` accepts `label`, 1 to 80 characters, and returns 400 `invalid_label` for another length. If a queued or running job writes the same output file name or data package name, the new job gets the suffix `-2`, `-3` and so on. The suffix shortens a long package name, so the name stays inside 64 characters. `POST /api/jobs/{id}/resume` returns 409 `output_in_use` when a queued or running job writes the output file of the job. |
| `serve` | (health route) | - | `GET /healthz` returns 200 with `{"status":"ok","version":"<version>"}`. It needs no token, so a monitor can read it. Another method returns 405 `method_not_allowed`. |
| `serve` | `--workers` | `1` | Number of jobs that run at the same time, 1 to 8. Another job waits in the queue with the state `queued`. The queue holds at most 50 jobs, and `POST /api/jobs` returns 429 `queue_full` above that. A value outside 1 to 8 fails at start. |
| `serve` | `--token` | `ATAKDL_TOKEN`, else generated | Token of a server that is not on loopback. A client sends it as `Authorization: Bearer <token>`, or signs in at `/signin` to get the `atakdl_token` cookie. With no token set, `serve` generates a 32-character token and prints it once to stderr. A server on loopback needs no token. 10 failed token attempts in one minute from one IP address give 429 `too_many_attempts`. |
| `download` | `--source` | - | Map source name (required) |
| `download` | `--bbox` | - | Bounding box: min_lat,min_lon,max_lat,max_lon. It is required, except with `--area` or `--resume --output <file>`. |
| `download` | `--area` | - | GeoJSON (`.geojson`, `.json`), KML (`.kml`) or KMZ (`.kmz`) file of the areas, at most 5 MiB. A polygon becomes a `polygon` area, and a line becomes a `route_buffer` area. The GeoJSON Feature properties `min_zoom`, `max_zoom`, `buffer_m` and `name` set the values of that area. A KML Placemark `name` becomes the area name. The command ignores Point features and prints a warning with their count. You cannot use it together with `--bbox`, or the command exits with code 1. |
| `download` | `--buffer` | `0` | Buffer in meters on each side of each line of the `--area` file that has no `buffer_m` property, from 10 to 50,000. |
| `download` | `--min-zoom` | 1 | Minimum zoom level. It must not be below the map source's minimum zoom level, or the command fails with `zoom_out_of_range`. |
| `download` | `--max-zoom` | 15 | Maximum zoom level. It must not be above the map source's maximum zoom level, or the command fails with `zoom_out_of_range`. |
| `download` | `--format` | `mbtiles` | Output format: `mbtiles`, `osmdroid` or `gpkg`. The extension of the default output file is `.mbtiles`, `.sqlite` or `.gpkg`. An unknown value fails with `invalid_output_format` and lists the valid values. A `gpkg` output file is a GeoPackage 1.3 raster in EPSG:3857, with one tile table named from the map source. GeoPackage holds only PNG and JPEG tiles, so `gpkg` for a map source with `webp` or `gif` tiles fails with `unsupported_tile_type`. An `osmdroid` output file is an OSMDroid tile cache in EPSG:3857 that ATAK reads, with the map source name in `provider`. Every output format accepts zoom levels up to 25, and a higher zoom level fails with `zoom_out_of_range`. `POST /api/jobs` accepts the same values in `output_format`, and returns 400 with the same error codes. |
| `download` | `--output` | `<data dir>/<name>.mbtiles` | Output file path. If the file exists and the command has neither `--resume` nor `--overwrite`, the command exits with code 3 and the file stays unchanged. A cancelled or failed job keeps the file and every tile it stored. |
| `download` | `--name` | - | File name of the output file, without the extension. The command keeps only `A-Z`, `a-z`, `0-9`, space, `-` and `_`, and each space becomes `_`. The output file is then `<data dir>/<name>_z<min zoom>-<max zoom>` with the extension of `--format`. With `--output`, the command ignores `--name`. |
| `download` | `--resume` | `false` | Continue the job into the existing output file. The job skips each tile that the file holds and counts it as `skipped`. If the file belongs to another map source, or if its output format is not the `--format` value, the command exits with code 3 and prints `output file is <format>` for a format mismatch. With no `--bbox`, the job reads its areas and zoom ranges from the `atakdl_areas` metadata row of the file. |
| `download` | `--overwrite` | `false` | Delete the existing output file before the job starts. You cannot use it together with `--resume`. |
| `download` | `--force` | `false` | With `--resume`, accept an output file that names no map source, for example a file from v0.1. |
| `download` | `--concurrent` | 5 | Concurrent downloads, from 1 to 32. A value outside this range fails with `invalid_concurrency`. |
| `download` | `--ignore-errors` | `false` | Accept the job at any failure rate. Without this flag, a job with a failure rate above 20% fails, and the command exits with code 5. With this flag, the command exits with code 0. |
| `download` | `--insecure` | `false` | Do not verify the TLS certificates of the tile server. The command prints a warning to stderr when this flag is set. |
| `download` | `--rate-limit` | `0` | Requests per second to the tile server. A value only lowers the policy rate of the map source, and `0` keeps it. A value above the policy rate gives the policy rate, with a note on stderr. A negative value fails with `invalid_rate_limit`. |
| `download` | `--accept-terms` | `false` | Accept the terms of use of a `restricted` map source. Without this flag, the command prints the reason and the reference URL for a restricted map source, and it exits with code 2. With this flag, it prints a warning to stderr and starts the job. |
| `download` | `--max-tiles` | `500000` | Tile cap of the job. If the job has more tiles, the command prints `too_many_tiles` and exits with code 4 before it sends a request. |
| `download` | `--data-package` | `false` | Wrap the output file in an ATAK data package, `<package name>.zip`, beside the output file. Import it on the device through ATAK's Import Manager. The zip holds `MANIFEST/manifest.xml` (Mission Package v2, with a new UUID v4 as `uid` and `onReceiveImport` set to `true`) and the output file. The command removes the unzipped output file after the zip is complete. If the zip step fails, the command prints `package_failed` and keeps the output file. An existing zip follows the `--resume` and `--overwrite` rules of the output file. With `--resume`, the job resumes the output file and then rebuilds the zip. `POST /api/jobs` accepts `data_package`, and the job record then names the zip in `output_file`. A failed zip step ends the job `failed` with `package_failed`. `GET /api/files` lists the zip with `format` set to `data_package`. |
| `download` | `--include-source` | `false` | With `--data-package`, add the source file of the map source to the zip. `POST /api/jobs` accepts `include_source`. |
| `download` | `--package-name` | - | With `--data-package`, the package name (required). The command trims the spaces around the name. It then holds 1 to 64 characters of `A-Z`, `a-z`, `0-9`, space, `_` and `-`. Each space becomes `_` in the file name. Another name fails with `invalid_package_name`. `POST /api/jobs` accepts `package_name`, and returns 400 `invalid_package_name` for a name that is not valid. |
| `estimate` | `--area`, `--buffer` | - | The same as for `download`. A feature with no zoom property gets the zoom range of `--zoom` or `--zoom-range`, and one of the two is then required. The command prints the tile count of each area and the unique tile count. |
| `estimate` | `--zoom` | `0` | One zoom level. A value above 0 overrides `--zoom-range` and `--max-tiles`. |
| `estimate` | `--zoom-range` | - | Zoom range as `min-max`, for example `10-12`. A value that is not `min-max` fails with exit code 1. The command removes each zoom level outside the zoom range of the map source and prints a warning. If no zoom level is left, it prints `No valid zoom levels for this source` and exits with code 1. |
| `estimate` | `--max-tiles` | `10000` | Tile cap of the automatic zoom level. With no `--zoom`, no `--zoom-range` and no `--area`, the command selects the highest zoom level from 1 to 18 that has at most this tile count, and never a level below 1. It prints `Auto-selected zoom level: <zoom>`. |
| `list-sources` | `--skipped` | `false` | List the source files that did not load, one line each: `path  reason  detail`. With no skipped source file, the command prints `No skipped source files.` |
| `tile-info` | `--lat` | - | Latitude of the coordinate (required), from -85.0511 to 85.0511. A value outside this range fails with exit code 1. |
| `tile-info` | `--lon` | - | Longitude of the coordinate (required), from -180 to 180. A value outside this range fails with exit code 1. |
| `tile-info` | `--zoom` | - | Zoom level (required), from 0 to 25. A value outside this range fails with exit code 1. The command prints the tile coordinates, the tile bounds and the resolution at the coordinate. It also prints the tile URL of the first three map sources that have this zoom level. |

## Progress and exit codes

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
