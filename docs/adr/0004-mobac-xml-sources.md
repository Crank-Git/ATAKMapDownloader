# 0004 — MOBAC XML source files from ATAK-Maps

Date: 2026-11-21
Status: accepted

## Context

Reconstructed on 2026-11-21 from the code during project-inherit; the original rationale was not recorded.

ATAK reads MOBAC `customMapSource` XML files for live map sources. The
[joshuafuller/ATAK-Maps](https://github.com/joshuafuller/ATAK-Maps) repository (MIT)
publishes a curated set of them. The README credits that repository. On 2026-02-06 the
project copied 33 source files into `mapsources/`. `internal/xmlparser` reads them at
run time from the directory that `--mapsources-dir` names.

## Decision

The project uses MOBAC XML as its map source format. It ships a copy of the ATAK-Maps
source files as a folder next to the binary.

## Consequences

- A solo operator can use the same source file in ATAK and in the project.
- The copy goes out of date. ATAK-Maps v2.3.1 (2026-10-31) has 53 source files in a new
  layout and has renamed every map.
- The parser reads only `customMapSource`. It drops the three `customWmsMapSource` files
  with no message.
- The binary is not self-contained, because it needs the folder.
- The planned `source-sync` feature revisits the copy, the layout and WMS support.
