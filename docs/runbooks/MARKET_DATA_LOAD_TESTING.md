# Runbook: Phase 1.1 Market-Data Release Evidence

## Scope

The harness is synthetic, deterministic, bounded, and makes no provider or
broker connection. It verifies classification, serial downstream delivery,
readiness stability, cancellation, and resource bounds. It cannot authorize
strategy evaluation or trading.

## Required GitHub Actions Gate

Trigger the manual workflow against the exact commit proposed for approval:

```sh
gh workflow run marketdata-load.yml --ref <branch-or-commit>
```

Every invocation runs on Ubuntu and always performs:

1. C-compiler discovery and runner capture.
2. `go test ./...`, `go vet ./...`, and `go build ./...`.
3. `go test -race ./...`.
4. Ten race-enabled repetitions of the bounded graceful-shutdown test.
5. Normal, burst, duplicate, late, malformed, and slow-consumer profiles.
6. A non-shortenable 30-minute real-time soak.

The workflow has a 60-minute job timeout and shorter per-command kill bounds,
including 32 minutes for the soak. This converts a panic/deadlock-style hang
into evidence before the job deadline, leaving time to upload it. One
repository-wide concurrency group uses `cancel-in-progress: false`, so a second
expensive run waits instead of interrupting or overlapping the first. Step
failures are retained long enough to build and upload evidence, but the final
enforcement step fails the job. Infrastructure, summary-generation, and
artifact-upload failures are failures, not successful application results.

## Local Commands

Run an individual deterministic profile with:

```sh
go run ./cmd/tradeedge-marketdata loadtest -profile=<name>
```

Valid names are `normal`, `burst`, `duplicate`, `late`, `malformed`,
`slow-consumer`, and `soak`. The soak profile always runs for 30 real minutes.
Use the reduced unit-test configurations for fast local verification; do not
alter the production soak default to shorten a release gate.

## Machine-Readable Evidence

The artifact is named:

```text
tradeedge-phase-1-1-release-gate-<run-id>-<run-attempt>
```

It contains:

- `phase-1.1-release-gate-summary.json`, the overall result, commit SHA, check
  outcomes, all profile reports, and explicit failure reasons.
- `marketdata-<profile>.json`, one stable-schema report for every profile.
- `marketdata-<profile>.log` and
  `marketdata-<profile>.resources.txt`, stderr and GNU `time -v` evidence.
- `ordinary-verification.log`, `race-detector.log`,
  `bounded-shutdown.log`, and `runner.txt`.

The soak report records configured duration and instruments; generated,
accepted, duplicate, rejected, malformed, late, quarantined, downstream, and
unique-downstream counts; unexpected loss and duplicate delivery; peak reorder
depth; starting, peak, and ending goroutines and heap; garbage collections;
readiness transitions and states; maximum cancellation time; processing time;
latency, backpressure, and final readiness; and pass/fail reasons.

Artifacts are retained for 90 days. Preserve the artifact ZIP, its GitHub run
URL, the commit SHA, and the summary JSON with the CEO approval record.

## Pass Conditions and Tolerances

The release gate fails unless all ordinary commands and race tests succeed and
every profile report has `passed: true`. Harness limits are:

| Evidence | Required condition |
| --- | --- |
| Delivery | accepted equals downstream and unique downstream; unexpected loss and duplicate delivery are zero |
| Classification | duplicate is exactly 20%, late exactly 5%, and malformed/rejected exactly 1% in their respective deterministic profiles |
| Quarantine | equals late plus malformed for the applicable profile |
| Consumer | never invoked concurrently; synchronous delay is measured as backpressure |
| Reorder buffer | peak no more than 10,000 events and no more than configured capacity |
| Heap | peak growth no more than 64 MiB |
| Soak heap | ending allocation no more than 16 MiB above the five-minute post-warm-up baseline |
| Goroutines | ending count no more than starting count plus two |
| Cancellation | maximum of five cancellation probes no more than 500 ms |
| Application shutdown | each of ten repetitions completes within the configured 1 s shutdown timeout plus 500 ms scheduling tolerance |
| Latency | normal p99 no more than 10 ms; burst p99 no more than 50 ms, excluding consumer work |
| Capacity | normal and burst processing at least 10,000 observations/second |
| Soak duration | measured duration no more than two seconds shorter than 30 minutes |
| Readiness | deterministic `WARMING_UP` then `READY`; final state equals expected state |

Memory uses explicit growth ceilings rather than exact before/after equality
because Go garbage collection and runner background activity vary. Capacity is
gated by the maximum-throughput normal and burst profiles; the real-time soak
is intentionally evaluated at its configured feed cadence.

## Failure Response

Do not rerun until a failure disappears without first classifying it. Retain
the failed artifact, determine whether the cause is code, race detection,
resource exhaustion, runner infrastructure, or evidence publication, and link
the investigation. Never weaken a threshold or classify missing evidence as a
pass. A panic, deadlock, timeout, missing JSON report, invalid JSON, missing
compiler, or artifact failure blocks approval.

## CEO Approval Evidence

Return all of the following:

1. GitHub Actions run URL and immutable commit SHA.
2. Overall workflow conclusion `success`.
3. Downloaded artifact name and the complete
   `phase-1.1-release-gate-summary.json`.
4. Soak report showing `configured_duration: "30m0s"` and `passed: true`.
5. Race and ordinary check outcomes from the summary.
6. Zero unexpected loss, zero unexpected duplicate delivery, bounded heap and
   goroutines, cancellation at or below 500 ms, and final readiness `READY`.
7. Any reruns, runner incidents, or observed nondeterminism, including failed
   artifacts rather than only the eventual passing run.
