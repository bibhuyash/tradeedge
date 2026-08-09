# Phase 6: Authoritative Position Accounting

## Scope

Phase 6 Milestones 1 and 2 convert authoritative provider-neutral OMS fills
into deterministic local positions and compare those positions with immutable
broker observations. The phase remains non-live and excludes valuation.

## Milestone 1: Position, Lot, and Realized P&L Accounting

- [x] Position identity is the versioned `(portfolio, canonical instrument)`
  identity; expired derivatives retain their historical instrument identity.
- [x] Immutable snapshots use signed net quantity, one bounded weighted-average
  open lot, integer minor-unit basis, cumulative trade totals, and gross
  realized P&L separate from unavailable charges.
- [x] Partial closes use deterministic integer division with remainder carried
  in open basis; the final close consumes the entire residual basis.
- [x] Long and short opens, increases, partial/full closes, and reversals through
  zero use checked integer arithmetic and publish no state on overflow.
- [x] Canonical ordering uses fill occurrence time, normalized receipt time, and
  fill identity. A late canonical predecessor fails closed and requires replay.
- [x] Fill application, position revision, and checkpoint share one optimistic,
  idempotent atomic publication boundary.
- [x] Exact duplicate fills are harmless; changed content under the same fill
  identity is an integrity failure.
- [x] Per-position serialization, bounded cross-position concurrency, bounded
  in-memory storage, cancellation, shutdown, restoration, and deterministic
  replay are covered by race-oriented tests.
- [x] Accounting packages contain no provider types, authoritative floating
  point, market valuation, broker-position repair, database, or live capability.

## Boundaries

M1 accepts an `AccountingFill` containing the immutable Phase 4 fill and
already-resolved portfolio, instrument, side, and normalized receipt evidence.
Resolving OMS lineage and continuously publishing fills is M2. Broker positions
are reconciliation evidence only and cannot enter the M1 mutation port.

Fees, taxes, market valuation, unrealized P&L, portfolio equity, daily MTM,
durable PostgreSQL storage, and automatic repair remain outside M1.

## Milestone 2: Fill Ingestion and Position Reconciliation

- [x] Only a committed OMS `Fill` with validated report, order, plan, intent,
  portfolio, instrument, side, and account-binding lineage enters accounting.
- [x] A versioned one-to-one portfolio-to-account binding preserves the M1
  `(portfolio, instrument)` position identity while making broker scope explicit.
- [x] Deterministic ingestion identity, source checkpoint/checksum, binding
  checksum, application, position revision, and accounting checkpoint share the
  M1 atomic publication transaction.
- [x] Exact committed retries are idempotent, in-progress duplicates are typed,
  and changed content under an existing identity fails as an integrity error.
- [x] Canonical predecessors are quarantined with rebuild-required evidence;
  M2 does not rewrite weighted-average history or implement repair.
- [x] Ingestion and reconciliation coordinators have fixed concurrency bounds,
  keyed suppression, cancellation, deadlines, shutdown, and restart-safe stores.
- [x] Provider-neutral immutable broker observations produce deterministic
  match, mismatch, local-only, broker-only, stale, or unknown evidence without
  access to accounting mutation or broker-order ports.
- [x] PAPER compares paper scope. SHADOW real observations are non-comparable;
  OFFLINE and LIVE_DISABLED fail closed.

M2 does not add market valuation, unrealized P&L, portfolio equity, durable
PostgreSQL, automatic correction, compensating orders, or live execution.

## Milestone 3: Deterministic Financial State

- [x] Latest eligible canonical quote LTP is the only mark; provenance,
  freshness, clock skew, and readiness are explicit with no silent fallback.
- [x] Immutable position valuations use checked integer open-basis arithmetic
  and cannot mutate quantity, cost basis, realized P&L, or fill history.
- [x] Atomic revisioned portfolio financial snapshots expose realized,
  unrealized, total P&L, directional exposure, and explicit completeness.
- [x] Partial, stale, and unavailable financial state is risk-ineligible;
  portfolio equity remains unavailable without authoritative capital.
- [x] Optimistic publication, idempotency, checkpoints, replay, bounded
  coordination, telemetry, GET-only operations, and release evidence close M3.

## Closure

Each milestone closes only after format, unit, deterministic replay, race/stress, vet,
build, dependency, secret, floating-point-authority, and live-capability gates
pass for the reviewed commit. Phase 6 closure does not authorize live trading
or Phase 7 runtime orchestration.
