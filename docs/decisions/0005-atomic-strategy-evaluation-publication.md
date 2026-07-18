# ADR 0005: Atomic Strategy Evaluation Publication

## Status

Accepted for Phase 2 Milestone 2.

## Scope

Define deterministic persistence, optimistic state revision control,
idempotency, integrity enforcement, checkpoint lineage, and restoration for
strategy evaluation effects.

## Assumptions

- The initial product scope is tens of instances and bounded in-memory test or
  paper workloads.
- One evaluation produces one next checkpoint and one evaluation record, plus
  at most one observation or advisory proposal.
- A later durable adapter may use PostgreSQL transactions without changing the
  consumer-owned repository contracts.

## Decision and Responsibilities

- Every instance starts with a verified revision-zero checkpoint.
- An evaluation that reads revision `N` proposes checkpoint `N+1`.
- The atomic publisher validates all identities and canonical payloads before
  entering the commit section.
- Under one lock it compares the expected revision, verifies current checksum,
  parent checksum, and prior-state hash, and prepares a complete next snapshot.
- One pointer replacement publishes the checkpoint, evaluation record, and
  optional output simultaneously.
- Evaluation ID is the publication idempotency identity. The repository stores
  canonical bytes and SHA-256 evidence. Exact matches return
  `IDEMPOTENT_REPLAY`; changed bytes return an identity-integrity error.
- Checkpoints use a versioned deterministic JSON envelope containing canonical
  runtime-state JSON and a SHA-256 checksum.
- Restoration verifies identity, version, configuration, instance revision,
  state schema, state revision, checksum, and parent lineage.

## Invariants

- Publication is all-or-nothing.
- Revision comparison and commit are atomic.
- A proposal or observation cannot exist without its evaluation and checkpoint.
- Cancellation and injected failure before the snapshot swap publish nothing.
- Concurrent writers cannot silently overwrite state.
- Returned values do not expose repository-owned byte slices or maps.
- Query ordering is deterministic.
- Repository contracts do not depend on the in-memory backend.

## Failure Modes

Stale revisions return a typed revision conflict. Reused identities with changed
content return a typed integrity failure. Corrupted prior or restored
checkpoints fail closed. Invalid cross-object identities are rejected before
commit. Capacity exhaustion and injected failures return an internal-storage
outcome without mutation.

## Trade-offs

Copying bounded in-memory maps before every commit consumes more CPU and memory
than mutating records in place, but provides a clear atomic boundary and makes
failure injection conclusive. SHA-256 and canonical JSON favor auditability over
compactness. The adapter is a reference implementation, not durable storage.

## Unresolved Questions

- Select the first durable backend and retention policy before production use.
- Define backup, restoration, and database transaction objectives.
- Approve repository capacity and alert thresholds for deployed paper trading.
- Runner timeout, panic quarantine, and operator recovery policy belong to
  Milestone 3.

## Acceptance Criteria

- Exact retries are idempotent and changed payloads under one identity fail.
- Two writers from the same revision produce one commit and one conflict.
- Checkpoint lineage and restoration mismatches are rejected.
- Injected failure and cancellation leave every repository view unchanged.
- Repeated and concurrent tests pass with deterministic ordering.
