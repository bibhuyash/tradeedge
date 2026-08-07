# Phase 6: Authoritative Position Accounting

## Scope

Phase 6 Milestone 1 converts immutable provider-neutral execution fills into
deterministic local positions and gross realized P&L. It remains paper-only and
does not ingest broker positions, value open positions, or authorize orders.

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

## Closure

M1 closes only after format, unit, deterministic replay, race/stress, vet,
build, dependency, secret, floating-point-authority, and live-capability gates
pass for the reviewed commit. Closure does not authorize Phase 6 M2 or live
trading.
