# Paper Trading

## Scope

Paper trading exercises the production domain pipeline with live or replayed market data and an in-memory or durable paper broker adapter.

## Assumptions

Paper fills cannot perfectly reproduce queue priority, impact, latency, exchange behavior, or broker failures.

## Responsibilities

The paper adapter models explicit order states, duplicate prevention, configurable fills, slippage, fees, partial fills, rejection, timeout ambiguity, positions, and reconciliation.

## Invariants

- Paper and live modes differ only at the broker adapter boundary.
- Phase 0 accepts paper mode only.
- Simulation limitations are visible in results.
- Paper success is evidence, not live authorization.

## Failure Modes

Optimistic fills, look-ahead bias, missing costs, non-deterministic tests, or failure to model partial legs can inflate performance.

## Trade-offs

Conservative simulation may understate performance but produces safer readiness evidence.

## Unresolved Questions

Fill models, fee schedules, slippage assumptions, and persisted replay formats belong to later phases.

## Acceptance Criteria

- No network or credential is needed for Phase 0 paper broker operations.
- Duplicate client requests do not create duplicate orders.
- Deterministic clocks and identifiers support repeatable tests.
