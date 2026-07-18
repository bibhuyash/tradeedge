# ADR 0001: Canonical Instrument Identity and File-Backed Market Data

## Status

Accepted for Phase 1.

## Context

Phase 0 embedded a provider token in `Instrument`, which made domain identity depend on Zerodha-style external identifiers. Phase 1 also needs deterministic historical datasets without prematurely selecting a PostgreSQL schema or adding a database driver.

## Decision

- A canonical `InstrumentID` is the full SHA-256 digest of a versioned, provider-neutral contract key.
- Provider tokens and trading symbols live in versioned, time-bounded mappings in the instrument master.
- Completed market events are immutable constructor-validated values with separate exchange and ingestion timestamps.
- Canonical datasets use atomically published directories containing a manifest, instrument master, and checksummed gzip NDJSON event and quality streams.
- In-memory and file repositories implement the same narrow dataset contracts.
- PostgreSQL persistence is deferred.

## Consequences

- Refetching the same observation produces the same event identity and replay order.
- Provider token changes do not change canonical instrument identity.
- File datasets are portable and inspectable but are not intended for concurrent analytical querying.
- Corrections create new dataset revisions rather than mutating published candles.
- Retention, licensing, PostgreSQL layout, and production calendar sources remain separate approvals.
