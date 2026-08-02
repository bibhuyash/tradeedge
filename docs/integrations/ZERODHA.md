# Zerodha Integration

## Scope

Zerodha is the initial market-data and future broker provider, isolated behind provider-neutral interfaces. Phase 5 M1 provides a read-only connectivity foundation; it is not composed into the default application runtime and contains no order capability.

## Assumptions

REST and streaming APIs can disconnect, throttle, duplicate, reorder, delay, or return ambiguous results. Provider identifiers and order state remain external facts.

## Responsibilities

The M1 adapter supports explicit authentication/session state, profile and
instrument reads, bounded timeouts/concurrency/safe-read retries, capability
discovery, mapping validation, readiness, redaction, and deterministic fakes.
Order state, lookup, updates, and reconciliation remain M2 work.

For market data, it must also report provider availability, preserve exact provider sequencing when available, maintain bounded socket-to-normalizer buffering, and fail readiness on overflow or stale delivery. Provider tokens remain mapping-table inputs and are prohibited from canonical IDs, operational responses, logs, and metric labels.

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

Retail access tokens expire at the documented daily session boundary and
require re-login after expiry or invalidation. M1 does not use refresh tokens.
Production secret storage, instrument licensing/retention, read-only runtime
composition, and operational login ownership remain deployment decisions.

## Acceptance Criteria

- Default runtime operation makes no Zerodha call and requires no credential.
- M1 adapter behavior is context-aware, bounded, redacted, and read-only.
- Provider-specific limitations are contained within the adapter.
- No M1 API can submit, modify, or cancel an order.
