# Phase 7 Runtime Release Gate

Run `go run ./cmd/tradeedge-phase7-gate` and retain its JSON output and SHA-256
with the reviewed commit. `passed` must be true and `failure_reasons` empty.

Before closure, `phase-7-runtime-release.yml` must pass formatting, full tests,
full Ubuntu race, ten focused runtime/replay and Phase 2-6 repetitions, vet,
build, and secret, provider-leak, floating-point-authority, live-mode, and
mutation-capability scans.

If readiness is lost, inspect `GET /api/v1/runtime/status`. Do not activate a
strategy manually, reinterpret an UNKNOWN order, skip restoration, substitute
a CAS/official-close price for LTP, or advance a fill cursor past failed
accounting. Restart only from a verified runtime manifest and reconcile all
required mode-specific evidence.

An unclean drain or corrupt manifest requires a halted restart. Phase 7 M1
evidence authorizes PAPER/SHADOW orchestration only.

## Phase 7 M3 full-day closure

Run `.github/workflows/phase-7-closure-release.yml` against the exact reviewed
commit. Preserve the run URL, artifact ZIP, `phase-7-closure.json`, and its
SHA-256. Phase 7 remains open unless `final_enforcement_result` is `PASSED`,
`passed` is true, the failure list is empty, and the commit and workflow
identity match the reviewed run. Missing, cancelled, skipped, or failed
evidence is a failure; do not reconstruct it by hand.

### Start of day

1. Confirm the binary commit and PAPER or SHADOW mode; reject any unknown mode.
2. Verify the calendar/version, clean checkpoint, restoration, mappings,
   reconciliation, market-data warm-up, valuation, risk controls, and kill
   switches before strategy activation.
3. For SHADOW, verify only read/stream credentials are present and the broker
   mutation call count remains zero. Telegram is optional and outbound-only.
4. Require runtime `READY` and separately verify each intended strategy's
   lifecycle state before activation.

### During the day

- Stale/unready data, missing mappings or valuation, and `UNKNOWN` block new
  exposure. A reconnect requires readiness and lifecycle revalidation.
- A strategy panic/timeout/invalid result halts that strategy. Portfolio
  controls or reconciliation disagreement halt the affected portfolio.
- Accounting conflict, corrupt lineage, or unavailable central authority halts
  the runtime. Never skip a cursor, infer timeout failure, or resubmit UNKNOWN.
- Telegram failure changes notification evidence only. Do not alter trading,
  restart repeatedly, or accept Telegram input as authority.
- During CAS, use calendar regimes and recorded provenance only. Restricted
  strategies cannot add exposure; only centrally approved reducing actions may
  continue. Never substitute official close or CAS prices for canonical LTP.

### End of day

1. Stop new exposure and resolve or explicitly retain every UNKNOWN/mismatch.
2. Reconcile, publish final valuation, CAS evidence, and the final EOD report.
3. Drain accepted work within the deadline, checkpoint all subsystem heads,
   verify the clean manifest, and require runtime/session `STOPPED`.
4. Preserve raw logs, scenario/failure/restart reports, resource evidence,
   closure JSON, checksum, workflow identity, and all failed reruns for 90 days
   or the separately approved durable retention period.

If any required evidence is unavailable, retain the failure, halt or drain at
the documented blast radius, and do not declare Phase 7 closed.
