# ADR 0006: Bounded Strategy Evaluation Runner

## Status

Accepted for Phase 2 Milestone 3.

## Scope

Define concurrency, serialization, duplicate prevention, readiness gating,
timeout and panic containment, atomic publication, and replay equivalence for
strategy evaluation. This ADR does not authorize lifecycle automation, risk,
allocation, executable quantity, orders, positions, broker access, or live
trading.

## Assumptions

- Initial capacity is tens of instances.
- At most four different instances evaluate concurrently by default.
- Strategy work is short, CPU-bound, deterministic, and observes context
  cancellation.
- Completed-candle replay and future live delivery can build the same immutable
  frame contract.

## Responsibilities and Decision

`EvaluateFrame(ctx, instanceID, frame)` is synchronous. A mutex-protected keyed
map admits one trigger per instance. A different trigger receives
`INSTANCE_BUSY`; the same trigger receives `DUPLICATE_IN_PROGRESS`. The key is
deleted on every terminal path. A fixed semaphore bounds work across instances;
there is no internal queue and caller blocking is explicit backpressure.

The trigger ID hashes schema version, instance revision, strategy version,
configuration hash, and frame ID. Evaluation and proposal identities derive
deterministically from accepted content. A committed evaluation is checked
before admission and returns `DUPLICATE_COMMITTED`. Failures that publish
nothing remain retryable. Because frame ID covers the trigger, ordered source
event IDs, logical time, subscription, master, calendar, and dataset revision,
changed frame content cannot retain the same runner trigger identity.

The runner uses Phase 1.1 readiness evidence and validates lifecycle,
subscription/frame identity, schemas, and candidate results. It applies a
100 ms engineering deadline and parent cancellation. Strategy invocation alone
is protected by panic recovery; panic text is limited to 256 bytes and the
stack to 8 KiB.

Candidate state is immutable and non-authoritative until the Milestone 2
publisher atomically commits checkpoint, evaluation, and optional output.
Revision conflict returns immediately without automatic re-evaluation.

Replay retains bounded per-subscription completed-candle buffers and invokes the
runner serially. An intermediate verified checkpoint can be restored before
continuing with remaining frames.

## Invariants

- One instance is never evaluated concurrently.
- Cross-instance concurrency never exceeds configuration.
- Admission creates no queue or goroutine per historical instance.
- Readiness-blocked input never invokes strategy code.
- Timeout, cancellation, panic, invalid output, conflict, and publication
  failure publish nothing.
- Only the atomic publisher makes candidate effects visible.
- Identical replay produces identical ordered records, IDs, checkpoint bytes,
  checksums, proposals, and final state.
- The runner and fixture have no broker, account, risk, allocation, order,
  position, provider SDK, filesystem, or network capability.

## Failure Modes

A non-cooperative Go strategy can ignore its context and exceed the deadline.
The runner deliberately does not spawn an unbounded kill goroutine; such a call
continues to occupy one bounded semaphore slot until it returns. Shutdown closes
admission, cancels accepted contexts, and waits only until the caller-provided
deadline. Repository corruption, readiness ambiguity, invalid schemas, and
revision conflict fail closed.

## Trade-offs

Synchronous calls and one keyed map favor bounded resource use, deterministic
backpressure, and simple shutdown over throughput. A process-local registry and
in-memory publisher are not durable production infrastructure. Cooperative
timeouts preserve the no-leak invariant but require reviewed strategies to
honor cancellation.

## Unresolved Questions

- CEO approval is required for production timeout, concurrency, failure
  quarantine, and operator recovery thresholds.
- Durable repository choice, retention, audit sink, and backup objectives
  remain open.
- Production instance/watchlist selection and lifecycle evidence policy remain
  unapproved.
- The engineering fixture has no profitability claim and cannot be promoted.

## Acceptance Criteria

- Runner outcome tests cover commit variants and every fail-closed terminal
  class.
- Race-heavy tests prove per-instance serialization and bounded parallelism.
- Panic and timeout are recoverable without advancing state.
- Committed and in-progress duplicates are distinct.
- Repeated and checkpoint-restored replay are byte stable.
- Ubuntu CI runs the full race suite and ten independent race-enabled
  strategy/replay tests. It uploads checksummed machine-readable evidence for
  concurrency, duplicate suppression, containment, publication integrity,
  replay equivalence, and bounded resources.
