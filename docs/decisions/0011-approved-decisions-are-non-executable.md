# ADR 0011: Approved Portfolio-Risk Decisions Remain Non-Executable

## Status

Accepted for Phase 3 Milestone 1.

## Decision

`PortfolioRiskDecision` has APPROVED, MODIFIED, REJECTED, and DEFERRED outcomes.
Positive outcomes contain bounded allocation authority and expiry, not broker
instructions. The contract excludes account IDs, broker tokens, client request
IDs, exchange order/product types, routing fields, and order payloads.

APPROVED requires an all-pass evaluation and authority exactly equal to the
allocation candidate. MODIFIED requires a modification result and authority
that is a strict subset of the candidate by capital, leg quantity, or both.
REJECTED requires a definitive violation. DEFERRED requires unavailable input,
cancellation, or another technical condition. Positive authority is valid no
later than its proposal, allocation candidate, or decision.

Every authority-bearing capital limit, leg bound, constraint, and validity
timestamp is included in canonical decision bytes, the decision checksum, and
the deterministic identity. Changing authority therefore cannot retain the
same decision identity.

## Consequences

No Phase 3 Milestone 1 value can bypass a future execution-intent boundary.
Approval alone cannot place an order or authorize live trading.
