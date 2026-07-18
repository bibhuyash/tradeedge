# MVP Scope

## Scope

The MVP consumes live Zerodha market data, evaluates multiple strategies, and runs the full portfolio, risk, execution, reconciliation, reporting, and alerting pipeline against a paper broker.

## Assumptions

- Zerodha is the initial provider but remains behind broker-neutral ports.
- PostgreSQL will be the durable system of record.
- One active modular-monolith process is sufficient initially.

## Responsibilities

- Normalize market data and detect staleness.
- Manage candidate, probation, active, suspended, and retired strategies.
- Model linked multi-leg intents without assuming atomic broker execution.
- Provide paper positions, P&L, reconciliation, kill-switch enforcement, and Telegram safety notifications.

## Invariants

- No real order route or real credential is part of the MVP.
- Every intent passes through eligibility, allocation, and central risk.
- Partial-leg exposure is visible and contained.
- Broker-reported actual orders and positions are authoritative.

## Failure Modes

The MVP must handle disconnections, duplicate events, timeouts, partial fills, rejected orders, restarts, missing notifications, and state divergence without creating duplicate exposure.

## Trade-offs

Realistic paper fills improve learning but cannot reproduce queue position, impact, or all broker behavior. The MVP favors conservative simulation.

## Unresolved Questions

- Initial instruments, capital model, and strategy-specific limits require approval.
- Data-retention periods and market-data licensing constraints require confirmation.

## Acceptance Criteria

- Live-data paper trading completes end to end.
- Reconciliation and global kill switch are demonstrable.
- Readiness gates are measurable and no live adapter is enabled.
