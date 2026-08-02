# Phase 4: Provider-Neutral Execution and OMS

## Scope

Phase 4 begins after an authoritative Phase 3 decision permits execution. The
target pipeline is:

`PortfolioRiskDecision -> ExecutionIntent -> OrderPlan -> Order -> ExecutionReport -> Fill`

Milestone 1 establishes deterministic contracts and atomic OMS persistence.
Milestone 2 makes those contracts operational through a bounded coordinator,
provider-neutral broker port, deterministic paper broker, and fail-closed
reconciliation. It exposes no HTTP operations and enables no live trading.

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

## Milestone 2 Checklist

- [x] Bounded cross-plan concurrency, keyed per-plan admission, and per-order
  serialization with deterministic dependency scheduling.
- [x] Runtime protective BUY-before-SELL enforcement and authority-expiry gate.
- [x] Provider-neutral submission, cancellation, event, lookup, and snapshot
  contracts with stable TradeEdge client identity.
- [x] Deterministic paper scenarios for fills, partial/delayed fills,
  rejection, cancellation, timeout, lost response, duplicate/out-of-order and
  late events, and temporary unavailability.
- [x] Submission idempotency, committed/in-progress duplicate handling, and
  explicit `UNKNOWN` outcome recovery without blind resubmission.
- [x] Provider-neutral reconciliation that repairs only broker-supported facts
  and blocks on absent, unknown, inconsistent, or incomplete evidence.
- [x] Broker reports and fills publish only through the M1 atomic OMS boundary.
- [x] Coordinator/paper checkpoints, deterministic replay continuation,
  cancellation, shutdown, panic, timeout, duplicate, race, and stress tests.
- [x] No Zerodha/Kite, credentials, live orders, positions/P&L, telemetry, or
  HTTP release capability.

## Deferred Milestone

- [ ] M3: telemetry, GET-only operational APIs, replay/stress evidence, and
  Phase 4 release gate.
