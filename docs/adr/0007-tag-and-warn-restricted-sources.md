# 0007 — Tag and warn for restricted map sources

Date: 2026-11-21
Status: accepted

## Context

Several map sources in the source catalog come from providers whose terms prohibit bulk download or offline caching: Google, Bing, Esri and the OSM tile servers. The references are in `docs/specs/features/06-polite-downloading.md`. Peer tools such as MOBAC and ATAK-Maps ship these source files with a disclaimer only. The maintainer considered three options: remove the map sources, keep them as they are, or tag them and warn.

## Decision

The project keeps restricted map sources, and it labels each one with a policy class from an embedded policy table. A restricted map source runs at 1 request per second, and the user must confirm with `--accept-terms` or a checkbox.

## Consequences

- The user, not the project, decides whether a job is allowed.
- The policy table must be maintained as provider terms change.
- A source file cannot set its own policy class, so a renamed host falls back to `conditional`.
