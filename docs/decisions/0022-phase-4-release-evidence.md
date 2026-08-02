# ADR 0022: Phase 4 Machine-Readable Release Evidence

## Status

Accepted for Phase 4 Milestone 3.

## Decision

Phase 4 closes only through an Ubuntu workflow bound to the reviewed commit.
It runs ordinary checks, the complete race suite, ten focused race/stress
repetitions, capability scans, bounded API/cardinality tests, and a deterministic
paper-only closure harness.

The authoritative artifact is
`phase-4-milestone-3-execution-release.json` with an adjacent SHA-256 file.
It records workflow identity, every required safety gate, resource bounds,
check outcomes, explicit failure reasons, and final enforcement status. Logs
and evidence are retained for 90 days.

Failed, cancelled, incomplete, malformed, hash-mismatched, or commit-mismatched
evidence cannot close Phase 4. Passing evidence authorizes no live broker or
real order capability.

## Consequences

The workflow uploads diagnostic evidence even on failure, then fails closed.
Local Windows checks cannot substitute for the Ubuntu race result.

## Rejected Alternatives

PR status alone is not a durable evidence contract. Hand-edited summaries are
not reproducible. A successful harness cannot compensate for failed race,
boundary, upload, or artifact-validation steps.
