# ADR 0029: Deterministic Trading Runtime Orchestration

## Status

Accepted for Phase 7 Milestone 1.

## Decision

A single in-process `TradingRuntime` owns admission, sequencing, readiness,
containment, cancellation, and drain across the released Phase 1-6 runtimes.
Released atomic repositories retain authority. The runtime checkpoint is a
checksummed manifest of their committed heads and cursors, not a competing
transaction or mutable aggregate.

Market admission is bounded. Existing keyed serialization and concurrency
limits remain authoritative inside each stage. Downstream saturation fails
closed and degrades readiness; authoritative events are not silently dropped.
Restoration and cross-head validation complete before any strategy activates.

## Consequences

Identical inputs, calendar, configuration, clock, broker observations, and
starting manifest traverse the same synchronous boundaries during replay and
runtime operation. A shared orchestration failure cannot manufacture a risk
decision, order, fill, position, or financial snapshot.

## Rejected Alternatives

Unbounded event buses, goroutine-per-entity scheduling, direct strategy-to-risk
or broker calls, and a second runtime-owned source of trading truth were rejected.

