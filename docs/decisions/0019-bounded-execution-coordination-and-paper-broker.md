# ADR 0019: Bounded Execution Coordination and Deterministic Paper Broker

## Status

Accepted for Phase 4 Milestone 2.

## Decision

Execution is owned by one bounded coordinator. It admits at most a configured
number of plans, serializes each plan and logical order, traverses plan legs in
canonical dependency order, and requires every protective BUY dependency to be
`FILLED` before submitting an exposure-increasing SELL. The execution intent
and plan remain the immutable upper bound; expiry fails closed at runtime.

The consumer-owned `BrokerPort` carries only TradeEdge domain identities and
integer quantities and prices. A deterministic, scripted paper adapter models
successful, partial, delayed, rejected, cancelled, unavailable, ambiguous,
duplicated, out-of-order, and late outcomes without goroutines or wall-clock
timing. Stable client-order identity makes an exact submit retry idempotent;
identity reuse with changed terms is rejected.

Submission transport ambiguity publishes `UNKNOWN`. The coordinator never
blindly resubmits it: it performs stable-client-identity lookup, verifies order
terms, and advances only when broker evidence resolves the uncertainty.
Timeout, cancellation, and recovered panic cannot invent a successful state.
All broker reports and fills enter through the M1 atomic OMS publication port.

## Consequences

Plans may wait behind the concurrency bound or protective dependencies. An
unresolved submission intentionally blocks progress. The in-memory paper
adapter is deterministic test infrastructure, not a live broker.

## Rejected Alternatives

One goroutine per order is unbounded. Retrying an ambiguous submission can
duplicate exposure. Broker IDs as primary keys prevent provider-neutral replay.
Submitting SELL legs before their protection creates avoidable naked exposure.
