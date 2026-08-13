# Risk Management

## Scope

The central risk engine validates every proposed allocation before execution and continuously guards portfolio safety.

## Assumptions

Risk configuration is versioned, conservative, and independently approved. Monetary calculations use integer minor units or exact decimals.

## Responsibilities

- Validate instrument, quantity, price, liquidity, market freshness, strategy eligibility, capital, concentration, loss, and operational health.
- Enforce account, portfolio, strategy, and order limits.
- Enforce the global kill switch before any submission.

## Invariants

- No order bypasses the central engine.
- Missing, stale, unknown, or inconsistent inputs reject new exposure.
- Thresholds are never relaxed merely to generate trades.
- Risk approval is bounded to the evaluated intent and expires.

## Failure Modes

Stale prices, delayed P&L, overflow, currency mismatch, configuration drift, concurrent allocations, and unavailable dependencies can understate exposure.

## Trade-offs

Fail-closed checks create false negatives during degraded operation but protect capital from unmeasured risk.

## Unresolved Questions

Numeric loss, exposure, liquidity, concentration, and decision-expiry limits require approved policy.

## Acceptance Criteria

- Decisions return explicit approve/reject outcomes and reasons.
- Inputs and policy versions are auditable.
- Kill-switch and inconsistent-state rejection cannot be bypassed.

## Phase 3 Milestone 1 Contract

Milestone 1 defines immutable `AllocationCandidate`, `RiskEvaluation`, and
`PortfolioRiskDecision` values with APPROVED, MODIFIED, REJECTED, and DEFERRED
outcomes. It defines pure rule inputs/results and typed bounded evidence but
does not execute rules, reserve capital, mutate a portfolio, or create an
execution intent. Unknown exposure remains explicitly unknown and therefore
cannot silently satisfy a future risk rule.

## Phase 8 M2 option validation

The Phase 3 catalog retains authority. Allocation is one long option lot and
uses option premium, lot size, instrument, and NIFTY underlying exposure.
STOP_NEW_EXPOSURE blocks entry but not an otherwise authorized close. Missing
expiry, mapping, freshness, or liquidity blocks before risk and never weakens a
threshold.

Phase 8 M3 records the released Phase 3 outcome and typed reason in SHADOW
evidence. Risk rejection creates no hypothetical open position. CAS,
STOP_NEW_EXPOSURE, kill switch, circuit-breaker, stale data, and mapping failures
remain fail-closed counters; qualification never weakens or replaces risk.
