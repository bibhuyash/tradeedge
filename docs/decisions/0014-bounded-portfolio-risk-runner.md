# ADR 0014: Bounded Portfolio Risk Runner

## Status

Accepted for Phase 3 Milestone 2.

## Decision

The runner is synchronous. One process-local keyed gate admits at most one
authoritative evaluation per portfolio and distinguishes identical in-progress
work from competing work. A fixed semaphore bounds work across portfolios
(default four). There is no internal queue and no goroutine per proposal.

Allocation runs once, then rules run sequentially in canonical policy order.
Sequential execution is the smallest deterministic model and makes timeout and
panic attribution explicit. A cooperative 100 ms default deadline covers the
evaluation. Panic recovery is limited to allocator and rule invocation. Any
technical error or invalid/unknown result defers and fails closed; cancellation,
timeout, or panic publishes nothing.

## Consequences

The same proposal, snapshot revision, configuration, instrument master, policy,
and injected logical time produce byte-identical candidate, results, decision,
checkpoint, and receipt. Non-cooperative code can occupy one bounded slot until
it returns, so reviewed rules must honor context cancellation. Production rule
catalog, telemetry, operational APIs, broker access, orders, and execution are
outside this milestone.
