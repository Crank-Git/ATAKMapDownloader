# Map sources

This page tells how `atak-dl` loads map sources: the embedded snapshot, the user source directory, `sources sync`, the old map source names and the skipped source files.

The binary embeds a snapshot of the source files of [ATAK-Maps](https://github.com/joshuafuller/ATAK-Maps) v2.3.1, by Joshua Fuller (MIT License). The files are MOBAC XML, in `internal/snapshot/maps/`. `internal/snapshot/NOTICE.md` credits ATAK-Maps, and `internal/snapshot/VERSION` holds the tag and the commit.

The snapshot holds 53 source files. One of them, `maps/base/blm/blm_satellite_ownership.xml`, has the root element `customMultiLayerMapSource`. The project skips it with `parse_error`, so 52 map sources load.

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

- `parse_error`: the file is not valid XML, its root element is not `customMapSource` or `customWmsMapSource`, or a `customWmsMapSource` has no `<layers>`.
- `missing_name`: the `<name>` element is empty or missing.
- `missing_url`: the `<url>` element is empty or missing.
- `unknown_placeholder`: the `<url>` element holds a URL placeholder other than `{$z}`, `{$x}`, `{$y}`, `{$q}` and `{$serverpart}`. These are the URL placeholders of the [ATAK-Maps XML reference](https://github.com/joshuafuller/ATAK-Maps/blob/v2.3.1/docs/xml-reference.md#url-placeholders). The letter case must match.
- `duplicate_name`: a file in the same directory has the same name. The first file in lexical path order loads.

## WMS map sources

A WMS map source always requests tiles in `EPSG:3857`. If its source file declares another CRS, the job first reads the GetCapabilities document of the server. If the server does not list `EPSG:3857` for the layers, the job stops with `unsupported_crs` before the first tile request.
