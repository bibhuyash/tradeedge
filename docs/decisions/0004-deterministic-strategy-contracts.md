# ADR 0004: Deterministic Strategy Contracts and Advisory Proposals

## Status

Accepted for Phase 2 Milestone 1.

## Scope

Define the provider-neutral boundary between validated market data and future
strategy execution without implementing a runner, strategy, risk engine, or
execution path.

## Assumptions

- Initial definitions evaluate completed candles, with single-stream and
  proportionate multi-instrument synchronization modes.
- Replay must reproduce identities when version, configuration, state, frame,
  logical time, and injected entropy are identical.
- Allocation and authoritative quantity calculation belong downstream.

## Decision and Responsibilities

- A definition version is the SHA-256 identity of its definition and all
  contract/schema versions.
- Configuration and runtime state are bounded canonical JSON objects. Object
  keys are sorted; duplicate keys, floating-point numbers, exponents, and
  trailing data are rejected.
- An instance revision binds a strategy instance, immutable version,
  configuration hash, and positive generation.
- Evaluation accepts an explicit immutable candle frame, readiness evidence,
  logical time, prior state, and injected entropy.
- Evaluation returns exactly one of `NO_ACTION`, `OBSERVATION`, or
  `TRADE_PROPOSAL`, plus a complete next state.
- A proposal is advisory. It uses normalized integer leg ratios, integer
  reference prices, and a bounded budget-basis-point sizing intent. Its stable
  ID is also its deduplication key.

## Invariants

- Strategy code has no broker, order, account, portfolio, risk, position,
  filesystem, or network capability in its interface.
- Required market data must be `READY`; missing or stale input cannot form a
  valid evaluation context.
- Completed candles are immutable and future candles are rejected.
- Configuration and state are at most 64 KiB; an evaluation frame is at most
  4 MiB; a proposal has at most eight legs and fifteen minutes of validity.
- Proposals contain no executable quantity and do not authorize trading.
- Updating a version or configuration creates a new identity; prior identities
  remain attributable.

## Failure Modes

Non-canonical state, schema mismatch, stale readiness, inconsistent frames,
future candles, malformed evidence, oversized data, unsafe sizing, duplicate
legs, and expired or excessive proposal windows fail validation. A future
runner must contain panics, timeouts, persistence failures, and duplicate
triggers before publication.

## Trade-offs

Returning the complete next state copies bounded data on every evaluation but
makes rollback and deterministic replay simpler than controlled mutation.
Canonical JSON is less compact than a custom binary format but is inspectable
and stable at the current scale. Explicit frames add construction work while
preventing accidental mixed-time inputs.

## Unresolved Questions

- Per-definition evaluation timeout and quarantine policies require approval.
- State/evaluation/proposal repository durability and retention remain a later
  Phase 2 milestone.
- Production sizing-policy floors and proposal-expiry ceilings require future
  risk and product approval.
- Lifecycle promotion evidence and operator authority remain unimplemented.

## Acceptance Criteria

- Identical contract inputs produce identical version, configuration, instance
  revision, frame, evaluation, and proposal identities.
- A definition cannot import provider or broker types through its interface.
- Every result is exclusive and includes a validated next state.
- Advisory proposals are explainable, bounded, deduplicable, and contain no
  executable order or quantity.
- Focused and repository-wide formatting, tests, vet, and build checks pass.
