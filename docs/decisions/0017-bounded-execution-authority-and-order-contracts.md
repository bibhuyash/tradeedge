# ADR 0017: Bounded Execution Authority and Deterministic Order Contracts

## Status

Accepted for Phase 4 Milestone 1.

## Decision

Only an unexpired Phase 3 `APPROVED` or `MODIFIED` `PortfolioRiskDecision`
with approved allocation bounds can create an `ExecutionIntent`. The intent
copies the decision identity and checksum and selects integer, lot-aligned
quantities and capital no greater than those bounds. Instruments, sides,
ratios, lot sizes, and validity cannot be changed or enlarged.
Intent creation also requires the authoritative post-decision portfolio
revision; any later or earlier revision makes the decision stale and fails
closed.

Intent, plan, leg, logical order, client-order, attempt, report, fill, and
publication identities are provider-neutral SHA-256 values over versioned,
length-framed canonical inputs. Broker order identifiers are opaque secondary
correlation values and never TradeEdge primary identities.

Plans use a deterministic acyclic dependency graph. Every exposure-increasing
SELL leg depends on every protective BUY leg in the plan. M1 does not execute
the graph.

Orders use the explicit states `CREATED`, `PLANNED`, `SUBMISSION_PENDING`,
`SUBMITTED`, `ACKNOWLEDGED`, `PARTIALLY_FILLED`, `FILLED`, `CANCEL_PENDING`,
`CANCELLED`, `REJECTED`, `EXPIRED`, `FAILED`, and `UNKNOWN`. Reports use typed
events and reasons. Fill totals are integer-only, monotonic, and bounded by the
order quantity. Stale reports may be retained without state regression; a
causally valid late fill can correct `CANCELLED` to a filled state.

## Consequences

Execution authority can be reduced or omitted for safety but never increased.
Unknown submission state remains explicit. Phase 4 contracts contain no broker
SDK, network call, credential, coordinator, position, or P&L capability.

## Rejected Alternatives

Broker-generated primary identities prevent deterministic pre-submission
persistence. Boolean lifecycle flags permit contradictory states. Blindly
accepting plan legs without an authority-subset proof would bypass Phase 3.
