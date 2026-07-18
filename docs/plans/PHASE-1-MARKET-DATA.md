# Phase 1 and 1.1: Market Data

## Scope

Build provider-neutral canonical instruments, historical quote/candle normalization, deterministic replay, and the operational hardening needed to detect whether data is expected, current, complete, verified, and safely published. No strategy, order execution, or live provider connection is included.

## Assumptions

- NSE session truth comes from an explicit versioned `Asia/Kolkata` calendar, never weekday inference.
- Phase 1.1 freshness values are test/hardening defaults and do not authorize trading.
- Canonical file datasets and memory repositories remain appropriate until retention and production storage decisions are approved.
- Prometheus client v1.23.2 is the only third-party Go dependency and is isolated to its adapter and HTTP composition.

## Responsibilities

1. Maintain canonical SHA-256 instrument identity separately from time-bounded provider mappings.
2. Normalize immutable quotes and completed OHLCV candles with exact integer prices.
3. Validate, suppress duplicates, quarantine late/malformed events, reorder within a bounded watermark, and abort on capacity exhaustion.
4. Generate expected candle windows from versioned trading days and session boundaries.
5. Evaluate required/optional streams at instrument, provider, watchlist, and global scope.
6. Store immutable checksummed revisions with deterministic build keys and verified parents.
7. Publish and roll back through append-only, compare-and-swap generations.
8. Replay serially with synchronous backpressure, rational speed, injected clocks, pause/resume, and cancellation.
9. Expose bounded read-only diagnostics and private-registry Prometheus metrics.
10. Run deterministic normal, burst, duplicate, late, malformed, slow-consumer, and real-time soak profiles.

## Invariants

- Provider tokens and SDK types never become canonical identity or cross adapter boundaries.
- Completed candles never mutate; invalid OHLCV never reaches consumers.
- Equal timestamps sort by provider sequence when both have one, then event ID.
- `UNKNOWN`, `INCOMPLETE`, `NO_DATA`, and `STALE` fail closed.
- `trading_permitted` is true only for configured global `READY`; `DISABLED` and `SESSION_CLOSED` are operational only.
- No candle crosses a calendar break and incomplete current windows are not marked missing.
- Published datasets and generations are immutable and checksummed.
- Slow consumers apply synchronous backpressure; consumers are never invoked concurrently.

## Failure Modes

Unknown mappings, malformed observations, duplicate/late data, buffer exhaustion, missing calendar coverage, holidays or modified sessions misrepresented by an input fixture, clock skew, independent exchange/ingestion/transport staleness, missing candles, checksum/schema failure, build-key disagreement, publication conflict, replay consumer failure, and cancellation all produce explicit errors or stable diagnostics.

## Trade-offs

Explicit daily calendars and full child revisions consume more operator effort and storage, but preserve deterministic evidence. File build-key scans and append-only catalogs are not optimized for high concurrency; repository contracts allow a later durable implementation. Private Prometheus exposition adds a maintained dependency but keeps metrics policy out of the domain.

## Implemented Milestones

- Phase 1 canonical identity, event model, validation, ordering, memory/file storage, and replay.
- Phase 1.1 calendar domain and strict fixture adapter.
- Calendar-aware gap detection and hierarchical readiness.
- Immutable correction lineage, idempotent rebuild, publication generations, and rollback.
- Typed telemetry, private Prometheus registry, `/metrics`, `/readyz`, and operational APIs.
- Deterministic load harness, fixtures, fuzz coverage, manual load workflow, ADRs, and runbooks.

## Operational Interfaces

The offline command supports `ingest`, `verify`, `replay`, `rebuild`, `publish`, `rollback`, `lineage`, and `loadtest`. Runtime endpoints are `/healthz`, `/readyz`, `/metrics`, and read-only `/api/v1/market-data/*` readiness, quality, calendar, dataset, lineage, and current-publication resources.

## Unresolved Questions

CEO approval is required for production freshness/warm-up/lag thresholds, the required watchlist, authoritative calendar source and correction SLA, market-data licensing/retention, endpoint authentication/exposure, scrape retention and alert ownership, reference performance hardware, publication/rollback roles, and production storage/backup objectives.

## Acceptance Criteria

- Explicit calendars deterministically represent holidays, breaks, modified hours, special sessions, and out-of-range coverage.
- Stale, missing, unknown, and incomplete data fail closed while closed markets are not falsely stale.
- Gaps derive from calendar windows, not adjacent candles.
- Corrections are immutable, lineage-preserving, checksum-verified, retry-safe, and atomically published; rollback retains evidence.
- Prometheus is adapter-only and prohibits high-cardinality/sensitive labels.
- APIs are GET-only, bounded, redacted, and return documented status classes.
- Load reports reconcile every observation, show no silent drop/concurrent consumer, and enforce approved manual thresholds.
- The manual Ubuntu release gate always runs race tests, ordinary verification,
  every deterministic classification profile, and the 30-minute real-time soak;
  it serializes expensive executions and uploads a commit-bound JSON summary,
  per-profile reports, logs, and resource evidence.
- Formatting, tests, race tests, vet, build, secret scan, and dependency-boundary review pass.
- No strategy, order execution, live Zerodha connection, credential, Kafka, Redis, Kubernetes, Grafana, Loki, or microservice is introduced.
