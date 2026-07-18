# Phase 1: Market Data

## Scope

Build provider-neutral canonical instruments, historical quote/candle normalization, quality enforcement, checksummed datasets, and deterministic replay. Live Zerodha connectivity remains deferred.

## Assumptions

Provider details will be verified against current official Zerodha documentation before coding. Market data is unreliable and may contain gaps or reordering.

## Responsibilities

- Load and version the instrument reference set.
- Resolve time-bounded provider mappings without using provider tokens as domain identity.
- Normalize historical observations into immutable quote and completed-candle contracts.
- Detect stale, duplicate, out-of-order, and missing data.
- Persist canonical datasets through in-memory and atomically published file repositories.
- Replay through injected clocks with rational speed, pause/resume, cancellation, and synchronous backpressure.

## Invariants

- Stale or uncertain data cannot produce tradable input.
- Provider SDK types remain inside the adapter.
- Invalid, duplicate, and too-late events cannot enter the downstream canonical stream.
- Equal timestamps use provider sequence when available and then event ID as a stable tie-break.
- No order placement or strategy implementation is introduced.

## Failure Modes

Unknown mappings, malformed observations, late events, buffer exhaustion, checksum failure, fixture corruption, stale data, and replay consumer failures are observable and fail closed.

## Trade-offs

Gzip NDJSON is portable and deterministic but is not a substitute for a future concurrent analytical store. PostgreSQL is deferred behind repository interfaces.

## Unresolved Questions

Instrument universe, candle construction, raw-data retention, licensing, and freshness thresholds require approval.

## Acceptance Criteria

- In-memory and file-backed paths produce the same normalized contracts.
- Gap and stale-data tests are deterministic.
- Repeated replay produces stable serial ordering and responds promptly to cancellation.
- No provider network connection, strategy, or order execution is introduced.
