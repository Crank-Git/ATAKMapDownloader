# 0004 — MOBAC XML source files from ATAK-Maps

Date: 2026-11-21
Status: accepted

## Context

Reconstructed on 2026-11-21 from the code during project-inherit; the original rationale was not recorded.

ATAK reads MOBAC `customMapSource` XML files for live map sources. The
[joshuafuller/ATAK-Maps](https://github.com/joshuafuller/ATAK-Maps) repository (MIT)
publishes a curated set of them. The README credits that repository. On 2026-02-06 the
project copied 33 source files into `mapsources/`. Epic 5 replaced that folder with the
embedded snapshot of ADR 0008. `internal/xmlparser` reads the snapshot and the user source
directory that `--mapsources-dir` names.

## Decision

The project uses MOBAC XML as its map source format. It ships the ATAK-Maps source
files as a snapshot in the binary (ADR 0008).

## Consequences

- A solo operator can use the same source file in ATAK and in the project.
- The snapshot goes out of date. `atak-dl sources sync` writes a newer release to
  `<user source directory>/atak-maps/`, and the user's own files replace it by name.
- The parser reads only `customMapSource`. It reports the five `customWmsMapSource` files
  of the snapshot as skipped. WMS support waits for #41.
- The binary is self-contained.
