# Phase 2 Strategy Release Gate

## Scope

This runbook closes Phase 2 Milestone 3 with Ubuntu race detection,
containment, concurrency, publication, replay, and bounded-resource evidence.
It does not authorize Phase 3, strategy activation, broker access, risk,
allocation, orders, positions, credentials, or live connectivity.

## Trigger

Push the reviewed commit, then run:

```sh
gh workflow run strategy-runner-stress.yml --ref <branch-or-commit>
gh run list --workflow=strategy-runner-stress.yml --branch=<branch> --limit=5
gh run view <run-id>
gh run download <run-id> \
  --name tradeedge-phase-2-milestone-3-<run-id>-<run-attempt>
```

The workflow permits only one expensive execution per Git ref. A newer
execution cancels an older execution for the same ref.

## Evidence Contract

`phase-2-milestone-3-strategy-stress.json` is authoritative. Its adjacent
`.sha256` file authenticates the downloaded bytes. The JSON records workflow
identity, ordinary and race results, ten repeated stress runs, committed
result classifications, duplicate suppression, readiness gating, containment,
concurrency, replay equivalence, goroutines, heap allocation, garbage
collections, cancellation/shutdown duration, artifact status, and explicit
failure reasons.

The workflow fails closed unless:

- the full race suite and all ten independent race repetitions pass;
- one strategy instance is never invoked concurrently and global concurrency
  stays at or below two in the closure harness;
- committed and in-progress duplicate triggers are each suppressed;
- readiness-blocked input never invokes strategy code;
- panic, timeout, cancellation, shutdown, invalid output, and revision
  conflict publish no candidate effect;
- there is zero unexpected result loss and zero duplicate publication;
- repeated replay is byte-stable and checkpoint continuation matches an
  uninterrupted replay;
- ending goroutines are no more than baseline plus two and peak goroutines are
  no more than baseline plus sixteen;
- ending heap growth is at most 16 MiB and peak heap growth is at most 32 MiB;
- maximum cancellation or shutdown latency is at most 500 ms; and
- both evidence uploads and authoritative JSON validation succeed.

Memory comparisons use explicit growth tolerances because Go runtime baseline
allocation and garbage-collection timing vary by runner. A failed, cancelled,
or infrastructure-interrupted GitHub Actions run is not release evidence.

## Responsibilities

The engineer verifies the downloaded SHA-256 and returns the authoritative
fields for approval. The approver confirms the commit SHA matches the reviewed
PR and that every failure-reason list is empty.

## Invariants

- The harness uses only canonical in-memory fixtures.
- It starts with no active production strategy.
- It has no broker, risk, allocation, order, position, account, credential, or
  provider-network capability.
- Workflow logs and artifacts contain no secrets.

## Failure Modes

Race findings, deadlocks, timeouts, malformed JSON, missing artifacts, changed
classification counts, resource-limit violations, or GitHub infrastructure
failure all leave Phase 2 unclosed. Rerun only after distinguishing a product
failure from an infrastructure failure; never rewrite a failed conclusion as
success.

## Trade-offs

Ten full strategy-package repetitions cost CI time but provide independent
race-enabled evidence. The compact closure harness complements rather than
replaces the complete unit and race suites.

## Unresolved Questions

Production concurrency and timeout policy, durable persistence, active
strategy selection, and operational alert ownership still require approval.

## Acceptance Criteria

The workflow conclusion is `success`, the summary SHA-256 verifies, all
enforcement fields pass, failure reasons are empty, and the commit is the
reviewed Phase 2 Milestone 3 head.
