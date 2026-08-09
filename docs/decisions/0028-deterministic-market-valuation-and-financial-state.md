# ADR 0028: Deterministic Market Valuation and Financial State

## Status

Accepted for Phase 6 Milestone 3.

## Decision

Phase 6 values authoritative positions using only the latest accepted canonical
quote last-traded price under the versioned Phase 1 freshness/readiness policy.
There is no candle, broker-price, bid/ask, or zero-price fallback. Closed-session
and pre-open marks are informational and risk-ineligible.

Checked integer arithmetic derives market value and unrealized P&L directly
from total open basis. Immutable position valuations bind the exact position
and market-data revisions. An immutable portfolio financial snapshot aggregates
realized/unrealized P&L and directional exposure with `COMPLETE`, `PARTIAL`,
`STALE`, or `UNAVAILABLE` readiness. Missing values never become zero.

Valuations, source manifests, financial snapshot, and checkpoint publish in one
optimistic transaction. Exact retries are idempotent; changed sources conflict.
Phase 3 consumes a provider-neutral, consumer-owned financial snapshot and
defers valuation-dependent decisions unless it is complete.

Portfolio equity remains unavailable because configured Phase 3 allocation
capital is not authoritative broker cash. Broker observations remain
reconciliation evidence and cannot enter valuation or accounting mutation.

## Consequences

Identical accounting, canonical market data, configuration, and injected time
produce byte-identical financial state. Valuation outages can block risk while
preserving accounting authority and the last committed snapshot.

## Rejected Alternatives

Automatic source fallback, floating-point averages, broker-position adoption,
partial totals presented as complete, and fabricated cash/equity all weaken
authority or replay guarantees.
