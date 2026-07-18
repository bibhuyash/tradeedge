# ADR 0003: Immutable Dataset Revisions and Publication Generations

## Status

Accepted for Phase 1.1.

## Scope

Define historical correction, publication, and rollback without rewriting evidence.

## Assumptions

Upstream history can be corrected, rebuild requests can be retried, and publication operators can race or lose connectivity.

## Decision and Responsibilities

- A correction is a complete child dataset with a verified parent, incremented revision, stable request ID, reason, source checksum, calendar version, and deterministic build key.
- Dataset identity excludes creation time and includes lineage, policy versions, source checksum, and content checksums.
- Publication is an append-only, checksummed generation catalog.
- Publication requires an expected-current dataset ID.
- Rollback appends a generation pointing to an older verified dataset.

## Invariants

Published dataset directories are immutable. The same build key and content is idempotent success; the same build key with different content is corruption. Temporary generations are never current.

## Failure Modes

Parent corruption, checksum mismatch, conflicting build content, stale expected-current IDs, missing generations, and partial temporary writes fail closed.

## Trade-offs

Scanning a file repository by build key is slower than an indexed database, but preserves deterministic local operation without prematurely adding a database driver.

## Unresolved Questions

Publication authority, correction approval roles, storage/backup objectives, and retention require CEO approval.

## Acceptance Criteria

Rebuild, publish, rollback, and lineage operations verify checksums; retry safely; preserve all generations; and reject lost updates.
