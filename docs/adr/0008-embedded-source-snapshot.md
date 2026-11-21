# 0008 — Embed a snapshot of ATAK-Maps source files

Date: 2026-11-21
Status: accepted

## Context

The project reads source files from a `mapsources/` folder that must sit next to the binary (ADR 0004). The folder is a February 2026 copy of 34 files. ATAK-Maps v2.3.1 has 53 files in a new layout and new map source names. The options were: embed a snapshot, download on first run, or keep the folder.

## Decision

The binary embeds a pinned ATAK-Maps snapshot. A user source directory overrides it by map source name, and `atak-dl sources sync` refreshes the user source directory from the ATAK-Maps releases.

## Consequences

- One binary carries a working source catalog and needs no internet at first run.
- Each project release pins one ATAK-Maps tag, and `NOTICE.md` credits ATAK-Maps (MIT).
- An alias table keeps the v0.1 map source names for one minor release.
- This supersedes the folder part of ADR 0004.
