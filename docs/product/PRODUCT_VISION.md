# TradeEdge Product Vision

## Scope

TradeEdge is a safety-first automated Indian-market options platform. It begins with live-data paper trading and may progress to controlled live trading only after objective readiness evidence and explicit approval.

## Assumptions

- Operators value capital protection, explainability, and recovery over trade frequency.
- Broker and exchange services are unreliable external systems.
- A valid outcome is `NO_TRADE`.

## Responsibilities

- Evaluate multiple strategies without allowing them to execute orders directly.
- Apply lifecycle eligibility, allocation, central risk, execution, and reconciliation consistently.
- Preserve an audit trail of every material decision and transition.

## Invariants

- Paper and live modes share one domain pipeline and differ only at the broker adapter.
- Risk rules are never weakened merely to keep a strategy active.
- Unknown broker state stops new exposure.
- Live trading is not available in the current phase.

## Failure Modes

Unsafe strategy evidence, stale data, ambiguous submission results, reconciliation mismatches, and failed operational dependencies all produce a fail-closed response.

## Trade-offs

The platform accepts fewer trades, additional latency, and greater operational complexity in exchange for deterministic controls and recoverability.

## Unresolved Questions

- Which instrument universe and trading sessions will the first paper strategies target?
- What independently approved evidence is required before any live-trading proposal?

## Acceptance Criteria

- Product decisions clearly prioritize safety and `NO_TRADE`.
- No document promises live trading or guaranteed returns.
- The shared pipeline and audit expectations are explicit.
