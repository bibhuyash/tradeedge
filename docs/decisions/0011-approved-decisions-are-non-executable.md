# ADR 0011: Approved Portfolio-Risk Decisions Remain Non-Executable

## Status

Accepted for Phase 3 Milestone 1.

## Decision

`PortfolioRiskDecision` has APPROVED, MODIFIED, REJECTED, and DEFERRED outcomes.
Positive outcomes contain bounded allocation authority and expiry, not broker
instructions. The contract excludes account IDs, broker tokens, client request
IDs, exchange order/product types, routing fields, and order payloads.

## Consequences

No Phase 3 Milestone 1 value can bypass a future execution-intent boundary.
Approval alone cannot place an order or authorize live trading.
