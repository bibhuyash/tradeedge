# Zerodha Integration

## Scope

Zerodha is the initial market-data and future broker provider, isolated behind provider-neutral interfaces. Phase 0 contains no Zerodha code.

## Assumptions

REST and streaming APIs can disconnect, throttle, duplicate, reorder, delay, or return ambiguous results. Provider identifiers and order state remain external facts.

## Responsibilities

A future adapter will authenticate, normalize instruments and events, enforce bounded timeouts and rate limits, map order states, support authoritative lookup, and redact sensitive data.

## Invariants

- No Zerodha SDK type crosses an adapter boundary.
- Strategies never receive the adapter.
- A submission timeout is reconciled before retry.
- Broker orders and positions are authoritative for actual external state.

## Failure Modes

Expired sessions, token leakage, stale instrument masters, stream gaps, throttling, ambiguous submissions, and inconsistent API views must produce explicit degraded states.

## Trade-offs

A provider-neutral boundary may not expose every Zerodha feature, but it protects domain behavior and testability.

## Unresolved Questions

Authentication renewal, exact rate limits, request correlation, historical-data use, and instrument licensing must be validated against current official documentation before implementation.

## Acceptance Criteria

- Phase 0 cannot make a Zerodha call.
- Future adapter behavior is context-aware, bounded, redacted, and reconcilable.
- Provider-specific limitations are contained within the adapter.
