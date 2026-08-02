# ADR 0018: Atomic OMS Order, Report, and Fill Publication

## Status

Accepted for Phase 4 Milestone 1.

## Decision

The consumer-owned OMS repository compares an expected order revision and
checkpoint checksum, re-applies the domain transition, validates parent/child
lineage, and atomically commits the execution report, optional fill, next order
checkpoint, and publication receipt.

Exact publication and report retries return `IDEMPOTENT_REPLAY`. Reusing any
publication, report, or fill identity with different canonical content is an
integrity failure. Duplicate reports and fills cannot repeat authoritative
effects. A stale revision is a typed conflict and is never silently rebased.

Checkpoints contain immutable canonical order bytes, state checksum, parent
checksum, and causative report/fill identities. Restoration verifies plan and
order lineage, checkpoint checksums, report/fill ownership, uniqueness, and
cumulative fill totals before exposing state.

The M1 adapter is a bounded, concurrency-safe in-memory reference store. One
lock and one commit section make failure injection conclusive. A future durable
adapter must preserve the same contracts with a database transaction.

## Consequences

Cancellation, validation error, revision conflict, capacity failure, or
injected storage failure publishes no partial authoritative state. Immutable
snapshot copying is acceptable at current paper-test scale but is not durable
storage.

## Rejected Alternatives

Separate order, report, and fill writes allow inconsistent state. Eventual
compensation cannot prove the required authority boundary. Last-write-wins
would hide concurrent order management.
