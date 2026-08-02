# ADR 0023: Read-Only Zerodha Connectivity Foundation

## Status

Accepted for Phase 5 Milestone 1.

## Decision

Zerodha connectivity is isolated in `internal/adapters/broker/zerodha`. The M1
surface contains only session-token exchange, profile reads, and instrument
master reads. It has no order submission, modification, cancellation, generic
request, OMS, coordinator, or `BrokerPort` implementation.

Credentials are loaded through an opaque source, format and log as redacted,
and are cleared from the session manager on shutdown. A restored access token
requires an explicit expiry. Expired retail sessions transition to `EXPIRED`
and require login; TradeEdge does not invent token refresh behavior.

Canonical `InstrumentID` remains authoritative. The existing instrument master
now supports canonical-to-provider lookup and rejects overlapping provider
tokens or overlapping mappings for one canonical instrument. The Zerodha
mapper rejects stale generations, missing mappings, ambiguity, and expired
derivatives.

All network operations are deadline-bounded and concurrency-bounded. Retries
exist only in the two fixed GET operations. Authentication uses only the Kite
session-token endpoint and is never retried by the adapter.

## Consequences

M1 can validate read-only connectivity without creating a live-order path.
Application composition, durable session storage, daily interactive login,
instrument-dump publication, WebSocket streaming, and all order behavior remain
later milestones or deployment decisions.

## Rejected Alternatives

A generic HTTP request method could make order endpoints reachable. Embedding
provider tokens in canonical instruments would violate Phase 1 identity.
Automatic refresh would claim behavior unavailable to ordinary retail Kite
sessions. Wiring the M1 adapter to `BrokerPort` would prematurely begin M2.
