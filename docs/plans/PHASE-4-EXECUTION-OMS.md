# Phase 4: Provider-Neutral Execution and OMS

## Scope

Phase 4 begins after an authoritative Phase 3 decision permits execution. The
target pipeline is:

`PortfolioRiskDecision -> ExecutionIntent -> OrderPlan -> Order -> ExecutionReport -> Fill`

Milestone 1 establishes deterministic contracts and atomic OMS persistence. It
does not submit orders, simulate a broker, reconcile broker state, expose HTTP
operations, or enable live trading.

## Invariants

- Only unexpired `APPROVED` and `MODIFIED` decisions provide authority.
- The authoritative portfolio revision must equal the decision's atomically
  published post-decision revision; revision drift fails closed.
- Selected execution capital and leg quantities are reduced-or-equal subsets.
- Instruments, sides, ratios, lot sizes, constraints, and validity cannot be
  silently changed.
- Stable TradeEdge identities exist before any future broker call.
- Illegal transitions, overfills, non-monotonic fills, stale revisions,
  corruption, and identity collisions fail closed.
- State, report, and fill publication is atomic and exact retries are
  idempotent.
- Protective BUY dependencies precede exposure-increasing SELL legs.
- `UNKNOWN` is explicit and cannot be guessed into failure.

## Milestone 1 Checklist

- [x] Execution intent and authority-subset validation.
- [x] Deterministic plan, leg, order, client-order, attempt, report, fill, and
  publication identities.
- [x] Explicit typed order state machine, transition reasons, partial fills,
  terminal behavior, stale reports, and late fills.
- [x] Deterministic acyclic multi-leg dependencies and BUY-before-SELL safety.
- [x] Provider-neutral OMS repository and optimistic order revisions.
- [x] Atomic order/report/fill publication with duplicate and collision rules.
- [x] Checksummed checkpoint and restoration contracts.
- [x] Bounded concurrency-safe in-memory reference adapter.
- [x] Deterministic, adversarial, atomicity, restoration, concurrency, and race
  tests.
- [x] No coordinator, broker adapter, reconciliation runner, telemetry, HTTP,
  position/P&L, credential, or live-execution capability.

## Deferred Milestones

- [ ] M2: bounded execution coordinator, deterministic paper broker,
  unknown-outcome recovery, and provider-neutral reconciliation.
- [ ] M3: telemetry, GET-only operational APIs, replay/stress evidence, and
  Phase 4 release gate.
