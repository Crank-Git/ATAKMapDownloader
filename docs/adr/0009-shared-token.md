# 0009 — Shared token for a server that is not on loopback

Date: 2026-11-21
Status: accepted

## Context

The maintainer wants the server to serve a small team on a LAN machine or in a container. Today the server has no authentication (ADR 0003). The options were: no authentication with a warning, one shared token, or user accounts.

## Decision

A server on loopback needs no token. A server on any other address requires one shared token from `--token` or `ATAKDL_TOKEN`, and generates one if neither is set. Clients send it as a bearer header or in a `SameSite=Strict` cookie.

## Consequences

- The solo operator on a laptop sees no change.
- Every team member has the same rights. The project has no per-user history or ownership.
- A user-account feature would supersede this record.
