# ADR 0024: Guarded Zerodha Order Adapter

## Status

Accepted for Phase 5 Milestone 2.

## Decision

`internal/adapters/broker/zerodha.ExecutionAdapter` implements the unchanged
Phase 4 provider-neutral `BrokerPort`. Provider request, order, and trade DTOs
exist only behind an injected transport boundary. TradeEdge execution and
client-order IDs remain authoritative; a broker order ID is retained only as
external correlation evidence.

The adapter accepts the Phase 3-approved submission without expanding its
quantity or terms and translates only the deliberately narrow NFO option,
NRML, regular, LIMIT, DAY profile. Mutation is controlled by a deny-by-default
gate and the adapter is not wired into normal application startup. No
unrestricted live runtime mode is introduced.

Every submission receives an exact request fingerprint and compact provider
tag before transport. Only a failure proven not sent maps to retryable
unavailability. A possibly sent request maps to `UNKNOWN`; repeating it returns
the same ambiguity without another transport call. Exact client identity and
order terms from broker order evidence can resolve it. Absence, ambiguity, or
term mismatch cannot.

Order updates become provider-neutral execution events in a bounded journal.
Stable event and trade identities suppress exact duplicates; changed duplicate
evidence, regressing/colliding state, stream gaps, overfills, and order/trade
mismatches degrade the snapshot instead of inventing state. Phase 4 continues
to publish every state/report/fill effect through its atomic OMS boundary.

Correlation and update state has an explicit versioned checkpoint and restore
contract. Session expiry is terminal until re-authentication. Rate limits and
transient read failures are bounded; potentially mutating calls are never
blindly retried by the adapter.

## Consequences

M2 can be tested against deterministic transports and restarted without losing
logical correlation when its checkpoint is durably supplied. The repository
still has no production order transport, default composition, or unrestricted
live mode. Durable storage, paper/shadow integration, WebSocket ownership,
operational gates, and release evidence remain M3.

## Rejected Alternatives

Using broker order IDs as OMS identity would break replay and idempotency.
Retrying ambiguous POSTs could create duplicate external orders. Publishing
provider updates directly into OMS would bypass Phase 4 atomicity. Treating a
missing snapshot row as rejection would fabricate broker state.
