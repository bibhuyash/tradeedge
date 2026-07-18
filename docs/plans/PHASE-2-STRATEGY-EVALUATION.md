# Phase 2: Strategy Evaluation Framework

## Scope

Phase 2 builds a deterministic, explainable, provider-neutral strategy
evaluation framework on canonical Phase 1.1 market data. Milestones 1, 2, and
3 are implemented: domain contracts, deterministic repositories, checksummed
checkpoint restoration, atomic evaluation publication, and a bounded runner
with replay integration and one engineering fixture.

It does not implement automatic lifecycle decisions, production strategy
promotion, allocation, Phase 3 risk policy, executable quantity, order
execution, live connectivity, or credentials.

## Assumptions

- Initial scale is tens of strategy instances, not thousands.
- Completed-candle evaluation is sufficient for the first framework fixture.
- Market-data readiness remains authoritative for whether evaluation inputs are
  usable.
- Future paper and live pipelines will consume the same proposal contract.

## Responsibilities

Milestone 1 owns:

- definition, version, instance, revision, configuration, lifecycle, and state
  identities;
- bounded canonical configuration and runtime-state encoding;
- single-stream, exact-close, and latest-completed subscription modes;
- immutable completed-candle frames with stable provenance;
- exclusive evaluation results and stable no-action reasons;
- explainable observations and bounded advisory multi-leg proposals;
- a deterministic, broker-neutral `strategy.Definition` interface.

Milestone 2 owns:

- provider-neutral definition/version, instance, checkpoint, evaluation,
  observation, proposal, and atomic-publication repositories;
- deterministic revision-zero checkpoints and verified restoration;
- optimistic `N` to `N+1` state progression;
- exact canonical retry idempotency and typed identity-integrity failures;
- a concurrency-safe bounded in-memory adapter using immutable snapshot swaps;
- deterministic ordered queries and test-only pre-commit failure injection.

Milestone 3 owns:

- an in-process version registry backed by accepted definition contracts;
- synchronous `EvaluateFrame`, per-instance serialization, keyed-state
  retirement, and a four-evaluation default cross-instance semaphore;
- deterministic trigger/evaluation/proposal identities and committed versus
  in-progress duplicate outcomes;
- Phase 1.1 readiness adaptation, lifecycle gating, a 100 ms cooperative
  deadline, parent cancellation, bounded panic diagnostics, and typed failures;
- candidate-state validation and publication solely through the Milestone 2
  atomic publisher;
- completed-candle replay framing with synchronous backpressure;
- provider-neutral telemetry, adapter-only Prometheus metrics, bounded GET-only
  diagnostics, and runner shutdown composition;
- the `NON_PRODUCTION_ENGINEERING_FIXTURE` moving-average crossover.

## Invariants

- Strategies cannot call brokers, construct orders, mutate positions, allocate
  capital, calculate executable quantity, or invoke risk/execution services.
- Strategy inputs contain canonical instrument IDs and immutable completed
  candles, never provider tokens or SDK types.
- Required input readiness is `READY`.
- Time, identity inputs, and entropy are explicit; system time and global
  randomness are absent from the definition contract.
- Money and prices use integer minor units; sizing uses integer basis points.
- Every evaluation yields one result and one complete next state.
- Checkpoint, evaluation record, and optional output publish together.
- An evaluation starting from revision `N` can publish only `N+1`.
- Identity reuse with changed canonical content fails as corruption; exact
  retries are idempotent.
- A `TRADE_PROPOSAL` is advisory and cannot authorize an order.
- `SUSPENDED` and `RETIRED` instances are not evaluable.
- `NO_TRADE` remains correct when no safe proposal exists.

## Failure Modes

- Invalid version or schema metadata makes a definition or context unusable.
- Non-canonical, floating-point, duplicate-key, or oversized configuration and
  state are rejected.
- Missing series, misaligned closes, stale candles, future candles, or failed
  readiness reject frame/context creation.
- Malformed evidence, duplicate legs, invalid integer sizing, or excessive
  validity reject proposals.
- Corrupted or mismatched checkpoints, stale revisions, cancellation, capacity
  exhaustion, and injected storage failure fail without partial publication.
- A readiness block, timeout, cancellation, panic, invalid result, conflict, or
  publication failure publishes no candidate effects.
- Timeout cancellation is cooperative; a strategy that ignores context can
  exceed its deadline, but the runner never creates an unbounded kill goroutine.

## Trade-offs

- SHA-256 identities and canonical JSON favor reproducibility and inspection
  over compactness.
- Complete returned state favors atomic replacement and failed-evaluation
  rollback over in-place performance.
- Exact-close frames provide strict consistency; latest-completed frames permit
  proportional strategies but preserve maximum-age checks.
- Full immutable snapshot copies make atomicity simple and testable at the
  approved scale, but are not intended for high-volume durable production.
- Synchronous evaluation makes backpressure and serialization explicit. It
  avoids queues and goroutine-per-instance retention at the cost of requiring
  callers to tolerate backpressure.

## Unresolved Questions

- Approve production evaluation timeout, repeated-failure quarantine, and
  operator-recovery policy. The 100 ms value is an engineering default only.
- Approve persistence retention and whether the first durable adapter remains
  file-backed before PostgreSQL.
- Approve the initial instance/watchlist set and lifecycle evidence policy.
- Approve production proposal-expiry and downstream sizing-policy ceilings.
- Confirm that the moving-average crossover remains an engineering fixture
  only and cannot be treated as validated trading edge.

## Acceptance Criteria

- Stable identities change when and only when identity-bearing inputs change.
- Canonical configuration and state reject floats and remain bounded.
- Frames enforce declared subscriptions, ordering, age, and synchronization.
- Evaluation context requires matching schemas, IDs, logical time, trigger, and
  ready market data.
- Results are exclusive and carry a complete next state.
- Proposals support one to eight legs, integer ratios/prices/basis points,
  bounded expiry, evidence, and stable deduplication identity.
- No strategy interface exposes broker, risk, allocation, execution, account,
  position, order, provider-token, network, or filesystem capability.
- Checkpoints serialize deterministically with SHA-256 integrity and verified
  parent lineage.
- Concurrent publication from one revision has exactly one winner.
- Failure injection and cancellation expose no partial checkpoint, record,
  observation, or proposal.
- `go test ./...`, `go vet ./...`, formatting, and build checks pass.
- Ten race-enabled runner/replay repetitions pass in the Ubuntu strategy stress
  workflow.

## Milestone 3 accepted-contract correction

Milestone 1 originally required every required series to contain exactly its
declared lookback. That made deterministic warm-up impossible. A required
series now contains between one and `Lookback` completed candles; zero remains
invalid. The runner and strategy decide `INSUFFICIENT_HISTORY` without inventing
data. This is the only accepted Milestone 1 contract changed by Milestone 3.
