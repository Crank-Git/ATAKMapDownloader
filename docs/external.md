# External facts

These are facts that agents need and that live outside the codebase. This file lists names and pointers only. It never holds a secret, a token or a credential, because this file is committed.

## Environment variables

Today no code reads an environment variable. Planned variables:

| Name | Read by | Meaning |
|---|---|---|
| `ATAKDL_TOKEN` | `serve` (feature 12) | The token for a server that is not on loopback. It has the same meaning as `--token`. |
| `ATAKDL_DATA_DIR` | `serve`, `download` (feature 12) | The directory for output files and `jobs.jsonl`. It has the same meaning as `--data-dir`. |

## CI and release secrets

| Name | Where | Meaning |
|---|---|---|
| `GITHUB_TOKEN` | GitHub Actions (automatic) | Publishes GitHub Releases and pushes the GHCR image. |

## Repositories and registries

- Code and tracker: https://github.com/Crank-Git/ATAKMapDownloader (public).
- Container image: `ghcr.io/crank-git/atak-map-downloader`.
- Upstream source files: https://github.com/joshuafuller/ATAK-Maps (MIT). The pinned tag is in `internal/snapshot/VERSION` after Epic 5.

## Third-party services the project calls

| Service | Used for | Policy |
|---|---|---|
| Nominatim (`nominatim.openstreetmap.org`) | `/api/geocode` | https://operations.osmfoundation.org/policies/nominatim/: at most 1 request per second, identifying User-Agent, caching. |
| Tile servers in the source catalog | Jobs | See the policy table in `docs/specs/features/06-polite-downloading.md`. |
| OpenStreetMap tiles | The online base map in the web UI | https://operations.osmfoundation.org/policies/tiles/ |
| GitHub Releases API | `atak-dl sources sync` (feature 09) | https://docs.github.com/en/rest/releases/releases. Unauthenticated requests are rate-limited per IP address. |

## Reference material

- ATAK-CIV source (archived at 4.6.0.5): https://github.com/deptofdefense/AndroidTacticalAssaultKit-CIV. It is the only public evidence for how ATAK reads output files.
- MBTiles 1.3: https://github.com/mapbox/mbtiles-spec/blob/master/1.3/spec.md.
- OGC GeoPackage 1.3: https://www.geopackage.org/spec131/.

## Support

Users report problems as GitHub issues on the repository.

## Device test

The maintainer has no TAK client installed. A device check on ATAK-CIV (Android) is optional and manual: copy the output file to `atak/imagery/` and confirm that the layer appears in Map Manager.
