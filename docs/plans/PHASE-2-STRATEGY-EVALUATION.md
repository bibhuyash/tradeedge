# Phase 2: Strategy Evaluation Framework

## Scope

Phase 2 builds a deterministic, explainable, provider-neutral strategy
evaluation framework on canonical Phase 1.1 market data. Milestones 1 and 2 are
implemented: domain contracts plus deterministic repositories, checksummed
checkpoint restoration, and atomic evaluation publication.

It does not implement a runner, timeout or panic isolation, replay integration,
an actual strategy, automatic lifecycle decisions, allocation, Phase 3 risk
policy, order execution, live connectivity, or credentials.

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

Later bounded milestones will own:

1. a bounded serial runner with trigger deduplication, readiness gating,
   timeout, panic containment, and failure quarantine;
2. replay integration, telemetry, and read-only operational diagnostics;
3. a moving-average crossover engineering fixture and determinism/property
   tests;
4. release evidence and operating documentation.

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
- Runner panics and timeouts remain explicitly deferred.

## Trade-offs

- SHA-256 identities and canonical JSON favor reproducibility and inspection
  over compactness.
- Complete returned state favors atomic replacement and failed-evaluation
  rollback over in-place performance.
- Exact-close frames provide strict consistency; latest-completed frames permit
  proportional strategies but preserve maximum-age checks.
- Full immutable snapshot copies make atomicity simple and testable at the
  approved scale, but are not intended for high-volume durable production.

## Unresolved Questions

- Approve default evaluation timeout, repeated-failure quarantine, and
  operator-recovery policy.
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
