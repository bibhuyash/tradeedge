# Phase 3: Portfolio and Risk Decisions

## Milestone 1 Contracts

Milestone 1 defines deterministic contracts and validation for:

`TradeProposal -> AllocationCandidate -> RiskEvaluation -> PortfolioRiskDecision`

Milestone 2 now consumes these contracts. It still contains no execution
intent, order, broker call, operational API, metric wiring, or live trading.

## Dependency Direction

```mermaid
flowchart LR
    S["strategy/model"] --> P["portfolio/model"]
    P --> R["risk/model"]
    PC["portfolio/config"] --> P
    RC["risk/config"] --> R
    PS["portfolio/storage"] --> P
    RS["risk/storage"] --> R
    PA["in-memory portfolio adapter"] --> PS
    RA["in-memory risk adapter"] --> RS
```

Domain packages do not import adapters, HTTP, Prometheus, execution, broker,
reconciliation, or provider packages. Risk rules receive immutable inputs and
cannot fetch data, call another rule, mutate state, use wall time, or generate
random identity.

The unused Phase 0 `Signal`, `Allocation`, and two-outcome `RiskDecision`
placeholders and their empty portfolio/risk ports were removed. They had no
consumers and were not Phase 2 contracts. Phase 2 proposal, storage, runner,
and replay APIs remain unchanged. Its canonical JSON implementation now
delegates to the shared implementation with identical accepted behavior.

## Authoritative Arithmetic

- Money and P&L use integer minor units.
- Capital buckets are non-negative and share one base currency.
- Signed P&L and net directional exposure are allowed.
- Percentages use integer basis points in `[0, 10000]`.
- Leverage uses a reduced integer rational.
- Checked add, subtract, and multiply fail on overflow.
- Unknown, unavailable, and not-applicable exposure are distinct from known
  zero.

The Milestone 1 capital invariant is:

`TotalCapital = AvailableCapital + ReservedCapital + DeployedCapital`

Current equity is:

`CurrentEquity = TotalCapital + RealizedPnL + UnrealizedPnL`

The first equation classifies capital usage; the second captures signed
mark-to-market effects. No broker balance or account concept is inferred.

## Immutable Portfolio Snapshot

A snapshot binds one portfolio revision to exchange/as-generated timestamps,
trading date, base currency, configuration identity/hash, capital buckets,
signed P&L windows, high-water mark, equity, deterministic strategy allocation
and exposure collections, control state, and a source checksum.

Collections are bounded, sorted, copied on input/output, and included in the
snapshot identity. Accounting disagreement, currency mismatch, invalid
control state, duplicate identity, overflow, or collection overflow rejects
construction.

## Exposure and Allocation Candidate

Exposure records identify a provider-neutral dimension and subject and retain
gross, net directional, premium, long, short, paid/received premium, maximum
loss knowledge, and overnight state. Current, incremental, and projected
exposure remain separate. Projection uses checked arithmetic, and unknown
maximum loss never becomes zero.

A strategy allocation is platform-owned. Strategy `SizingIntent` is advisory.
An allocation candidate binds the proposal and portfolio revision to bounded
capital, premium/risk knowledge, optional canonical-master quantity bounds,
incremental/projected exposure, reserve impact, rounding evidence,
constraints, and typed reasons. It neither reserves capital nor mutates a
snapshot. Quantity bounds are portfolio constraints, not executable order
quantity.

## Risk Contracts

Risk policies contain a bounded, statically registered set of unique versioned
rules in contiguous deterministic order. Unknown or duplicate rules and
conflicting order fail configuration.

Rules implement one pure contract:

```text
Evaluate(context.Context, RiskRuleInput) RuleResult
```

Results are exactly `PASS`, `VIOLATION`, `MODIFICATION_REQUIRED`, `DEFER`, or
`ERROR`. Technical errors are not ordinary policy violations. Evidence is a
bounded typed structure with availability, observed/limit/projected/headroom
values, comparator, unit, subject, source identities, formula version,
timestamp, and explanation.

Violations carry an explicit severity and effect. Severity never implicitly
selects behavior. Policies are fail-closed.

## Decision Boundary

`PortfolioRiskDecision` supports `APPROVED`, `MODIFIED`, `REJECTED`, and
`DEFERRED`.

- APPROVED contains allocation bounds equal to the candidate.
- MODIFIED contains a strict subset of candidate capital or leg bounds and
  explicit constraints; it can never increase either bound.
- REJECTED and DEFERRED contain no approved allocation.
- Every outcome references one proposal, portfolio snapshot/revision,
  allocation candidate, risk evaluation, policy/configuration hashes,
  ordered reasons and violations, evidence checksum, generation time, and
  expiry.

An approved decision is an internal authorization artifact only. It contains
no account, broker token, broker request, exchange order/product type, client
request ID, or order payload and cannot be submitted to a broker.

All approved capital, leg bounds, constraints, and validity participate in the
canonical decision bytes, checksum, and identity. Outcome validation is tied
to the aggregate rule statuses: all-pass for APPROVED, at least one safe
modification for MODIFIED, a definitive violation for REJECTED, and a defer or
technical failure for DEFERRED.

## Configuration and Persistence

Portfolio and risk configuration use the same accepted canonical JSON
mechanism as Phase 2. Documents are at most 256 KiB, integer-only, depth and
collection bounded, and strict about duplicate and unknown fields.

Repositories provide deterministic immutable registration and reads, typed
not-found/capacity/collision errors, exact duplicate idempotency, defensive
copies, cancellation, and deterministic enumeration. The in-memory adapters
are reference implementations; they deliberately do not fake the future
transactional boundary.

## Bounded Assumptions

- one portfolio and one base currency;
- 1-5 definitions and at most 20 strategy instances;
- 10-100 instruments and fewer than 100 exposure records;
- fewer than 50 rules and 10 proposals per minute;
- fewer than 10,000 decisions per trading day;
- snapshot and canonical decision documents below 1 MiB;
- later allocation/risk runtime target below 10 ms;
- runtime timeout and durable persistence are deferred.

## Milestone 2 Runtime

`TradeProposal + PortfolioSnapshot(N) + configuration + logical time` enters a
synchronous decision runner. Allocation is deterministic and integer-only.
Rules execute sequentially in canonical policy order; technical errors remain
distinct from violations and invalid results fail closed. A keyed portfolio
gate prevents overlapping mutation and a fixed semaphore bounds different
portfolios (default four). Evaluation has a cooperative 100 ms default timeout
and recovers panics only around allocator/rule code.

The provider-neutral runtime transaction commits candidate, evaluation,
decision, optional capital reservation, and checkpoint/snapshot `N+1` together
after optimistic revision and lineage validation. An exact committed trigger is
idempotent; an in-progress duplicate is suppressed; stale input returns a typed
conflict without automatic reevaluation. APPROVED/MODIFIED reserve bounded
capital in the new authoritative snapshot. REJECTED/DEFERRED advance the audit
revision without reserving capital. Neither decision outcome is executable.

Replay calls the same runner serially. Verified checkpoint restoration produces
the same subsequent decision bytes and final state as uninterrupted replay.

## Milestone 3 Production Controls and Release

Milestone 3 adds the reviewed ten-rule production-style catalog, provider-neutral
risk telemetry, bounded GET-only operational APIs, projected instrument,
underlying, and portfolio-wide exposure, and machine-readable race/stress
release evidence. Unknown or missing risk data remains fail-closed. Rules remain
pure; portfolio mutation remains inside the Milestone 2 atomic publication.

The reference repositories remain bounded and in-memory. This milestone adds no
broker, execution intent, OMS, order, fill, position, live-trading, credential,
or durable database capability and does not begin Phase 4.

## Milestone 1 Completion Checklist

- [x] Immutable revisioned portfolio snapshots and capital accounting.
- [x] Integer-only checked money, ratios, quantities, P&L, and exposure.
- [x] Explicit current, incremental, projected, unknown, and unbounded risk.
- [x] Platform-owned strategy allocations and non-reserving candidates.
- [x] Pure provider-neutral rule, evidence, violation, and evaluation contracts.
- [x] Deterministic non-executable decisions with four exclusive outcomes.
- [x] Kill-switch and circuit-breaker state contracts.
- [x] Bounded canonical portfolio and risk configuration.
- [x] Provider-neutral repositories with idempotency and collision rejection.
- [x] Deterministic identity, canonical-output, overflow, boundary, and
  repository contract tests.
- [x] No runner, orchestration, mutation, reservation, replay, telemetry, HTTP,
  production rule, broker, OMS, order, position, or execution capability.

## Milestone 2 Completion Checklist

- [x] Deterministic allocation and ordered pure-rule evaluation.
- [x] Four-outcome aggregation and non-executable decisions.
- [x] Per-portfolio serialization and bounded cross-portfolio concurrency.
- [x] Committed/in-progress duplicate handling and exact retry idempotency.
- [x] Typed stale-revision conflicts without implicit reevaluation.
- [x] Atomic decision, optional reservation, and checkpoint publication.
- [x] Timeout, cancellation, panic, invalid output, and storage failure publish nothing.
- [x] Deterministic replay and checkpoint continuation equivalence.
- [x] Bounded in-memory provider-neutral reference repository with defensive copies.
- [x] No production rules, telemetry/API wiring, broker, OMS, order, position, or execution capability.

## Milestone 3 Completion Checklist

- [x] Pure deterministic capital, allocation, loss, drawdown, exposure, reserve, kill-switch, and circuit-breaker rules.
- [x] Checked integer-only calculations, typed bounded evidence, and fail-closed unknown-input behavior.
- [x] Safe lot-aligned `MODIFICATION_REQUIRED` results where resizing can repair a breach.
- [x] Provider-neutral risk telemetry with bounded Prometheus dimensions.
- [x] GET-only, timeout-bounded, list-bounded risk operational APIs.
- [x] Deterministic replay/checkpoint, duplicate/conflict, atomicity, containment, and concurrency regression coverage.
- [x] Race/stress workflow with versioned JSON and SHA-256 release evidence.
- [x] Forbidden capability, credential, broker/execution, and authoritative floating-point scans.
- [x] No Phase 4, broker, OMS, order, fill, position, execution, live-trading, or durable-database capability.
