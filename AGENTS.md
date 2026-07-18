# TradeEdge Agent Instructions

## Mission

TradeEdge is a safety-first automated options-trading platform for the
Indian market. The system will begin with live-data paper trading and
will progress to controlled live trading only after objective readiness
criteria are satisfied.

Correctness, capital protection, auditability and recoverability take
priority over speed of implementation.

## Current Phase

The project is currently in the architecture and foundation phase.

Unless a task explicitly says otherwise:

- Do not place real orders.
- Do not enable live trading.
- Do not use real credentials.
- Use paper broker implementations and test fixtures.
- Treat all broker and exchange calls as unreliable external operations.

## Technology Direction

- Primary language: Go
- Initial architecture: modular monolith
- Database: PostgreSQL
- Cache and coordination: Redis only where justified
- Messaging: introduce only when demonstrated load or isolation requires it
- Broker: Zerodha behind a broker-neutral interface
- Notifications: Telegram behind a notification interface
- Deployment: containerized Linux environment with a static outbound IP
- Observability: structured logs, metrics, traces and append-only audit events

Do not introduce Kafka, Kubernetes or microservices without a documented
requirement and explicit approval.

## Domain Pipeline

The intended pipeline is:

Market data
→ strategy evaluation
→ signal
→ strategy eligibility
→ portfolio allocation
→ pre-trade risk validation
→ execution intent
→ order state machine
→ broker interaction
→ reconciliation
→ positions, P&L, alerts and audit events

Never allow a strategy implementation to call a broker directly.

## Mandatory Safety Rules

1. All orders must pass through the central risk engine.
2. All broker operations must use idempotency or duplicate-prevention logic.
3. Broker state is authoritative for actual orders and positions.
4. Internal state must be reconciled against broker state.
5. Unknown or inconsistent state must stop new order placement.
6. Credentials must never be committed.
7. Logs must not contain access tokens, API secrets or sensitive account data.
8. Every material automated decision must be auditable.
9. Every production execution path must support kill-switch enforcement.
10. Paper and live trading must use the same domain pipeline, differing only
    at the broker adapter boundary.
11. Do not silently weaken risk thresholds to keep a strategy active.
12. Never infer that an order failed merely because the API response timed out.

## Engineering Rules

- Keep domain logic independent from broker SDKs and HTTP handlers.
- Prefer explicit types and state machines over Boolean flags.
- Use context cancellation for external calls.
- Apply bounded timeouts.
- Retry only operations that are safe to retry.
- Use deterministic tests for trading and risk logic.
- Inject clocks and ID generators when time or identity affects tests.
- Avoid package-level mutable state.
- Return typed errors where callers need behavioural decisions.
- Wrap errors with operational context.
- Keep interfaces small and consumer-owned.
- Use integer minor units or a decimal representation for monetary values.
- Do not use binary floating point for authoritative money calculations.

## Order Safety

Model order execution as a state machine. At minimum consider:

CREATED
RISK_APPROVED
SUBMISSION_PENDING
SUBMITTED
ACKNOWLEDGED
PARTIALLY_FILLED
FILLED
CANCEL_PENDING
CANCELLED
REJECTED
UNKNOWN

UNKNOWN is a real operational state. Resolve it through broker lookup or
reconciliation before retrying submission.

## Definition of Done

A task is complete only when:

- The implementation matches the relevant documentation.
- Unit tests cover normal and important failure paths.
- The appropriate test, lint and formatting commands pass.
- No secrets are introduced.
- Operational and safety implications are documented.
- The final response summarises changed files, tests run, assumptions and
  unresolved risks.

## Working Method

For architectural or multi-file tasks:

1. Read AGENTS.md and relevant files under docs/.
2. Inspect the existing repository before proposing changes.
3. Produce or update an implementation plan.
4. Identify safety and failure-mode implications.
5. Implement one bounded milestone.
6. Run tests and static checks.
7. Review the diff for regressions.
8. Update documentation when decisions or behaviour changed.

Do not implement multiple major milestones in one task unless explicitly asked.

## Important Documentation

Read the relevant files before implementation:

- docs/product/PRODUCT_VISION.md
- docs/product/MVP_SCOPE.md
- docs/architecture/SYSTEM_ARCHITECTURE.md
- docs/trading/RISK_MANAGEMENT.md
- docs/trading/STRATEGY_LIFECYCLE.md
- docs/trading/PAPER_TRADING.md
- docs/reliability/ORDER_SAFETY.md
- docs/reliability/FAILURE_RECOVERY.md
- docs/decisions/