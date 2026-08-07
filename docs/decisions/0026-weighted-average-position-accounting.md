# ADR 0026: Weighted-Average Authoritative Position Accounting

## Status

Accepted for Phase 6 Milestone 1.

## Decision

Authoritative positions are grouped by portfolio and canonical instrument.
Immutable execution fills are ordered by occurrence time, normalized receipt
time, then fill identity. Weighted-average cost is represented by total open
basis divided by absolute open quantity; snapshots therefore need only one
bounded aggregate open lot.

Prices, basis, quantities, and realized P&L use checked integers. A partial
close allocates `floor(open basis * closed quantity / open quantity)` and leaves
the remainder in open basis. A final close consumes all remaining basis. A
reversal closes existing exposure before opening the remainder on the opposite
side. Opening exposure never realizes P&L, and a flat position has zero basis.

Gross realized P&L is authoritative. Brokerage, STT, exchange charges, GST,
stamp duty, and other fees remain unavailable rather than estimated. Market
prices and unrealized P&L are excluded.

One provider-neutral transaction re-applies the accounting transition and
atomically commits the immutable fill application, next position revision, and
checkpoint. Exact retries are idempotent; identity collisions, stale revisions,
out-of-order predecessors, corrupt lineage, and overflow fail closed.

## Consequences

Accounting is replay-stable, bounded, and independent of broker SDKs. Arrival
of a canonical predecessor after later fills requires verified replay instead
of an in-place correction. Trading-account scope requires a future versioned
identity migration after account identity exists in authoritative execution
contracts.

## Rejected Alternatives

FIFO requires an unbounded open-lot collection for M1. Floating-point average
prices are not replay-safe. Per-close rounding without remainder carry can lose
basis. Direct broker-position writes bypass immutable fill evidence and audit.
