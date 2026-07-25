# ADR 0007: Portfolio and Risk Decision Boundary

## Status

Accepted for Phase 3 Milestone 1.

## Decision

The deterministic domain path is `TradeProposal -> AllocationCandidate ->
RiskEvaluation -> PortfolioRiskDecision`. Strategy proposals remain advisory.
Portfolio and risk types import provider-neutral strategy values but expose no
broker, account, order, reconciliation, HTTP, or telemetry capability.

APPROVED and MODIFIED are internal authorization artifacts. They are not
execution intents and cannot be submitted to a broker.

## Consequences

Every decision remains attributable to one proposal and one immutable
portfolio revision. Proposal consumption, authoritative mutation, reservation,
and execution are deferred.
