# Phase 4 Execution Release Gate

## Scope

This gate closes provider-neutral paper execution and OMS. It does not add or
authorize Zerodha/Kite, credentials, real orders, positions/P&L, or live mode.

## Procedure

Run `Phase 4 Execution Release` against the reviewed head. A newer run may
cancel an older run for the same ref. Download the artifact named
`tradeedge-phase-4-milestone-3-<run-id>-<run-attempt>` and verify the SHA-256
beside `phase-4-milestone-3-execution-release.json`.

Confirm that the artifact commit equals the reviewed head, all ordinary/full
race/ten repeated race steps succeeded, every named safety gate is `passed`,
`final_enforcement_result` is `passed`, and `failure_reasons` is empty.

## Fail-Closed Conditions

Any state-machine, authority, dependency, idempotency, UNKNOWN, fill,
reconciliation, atomicity, replay, checkpoint, concurrency, containment,
shutdown, cardinality, API, capability-scan, race, upload, JSON, checksum, or
commit mismatch leaves Phase 4 open. Diagnose infrastructure failures rather
than rewriting them as product success.

## Acceptance

Only the reviewed commit plus a successful workflow and validated artifact
closes Phase 4. Closure remains paper-only and grants no Phase 5 implementation
or live-trading approval.
