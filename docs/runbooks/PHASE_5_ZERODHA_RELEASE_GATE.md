# Phase 5 Zerodha Release Gate

Run `Phase 5 M3 Zerodha Release` against the reviewed commit. Download the
`tradeedge-phase-5-m3-<run-id>-<attempt>` artifact and verify the SHA-256 beside
`phase-5-milestone-3-release.json`.

Confirm the artifact commit matches the reviewed head, ordinary and full-race
checks passed, all ten focused race/recovery repetitions passed, every named
gate is true, `failure_reasons` is empty, and `final_enforcement_result` is
`passed`.

Any mode, mutation-boundary, session, mapping, stream, UNKNOWN,
reconciliation, checkpoint, concurrency, cleanup, redaction, API, telemetry,
race, artifact, checksum, or commit failure leaves Phase 5 open. PAPER and
SHADOW promotion is restart-only. Session expiry or stream/reconciliation gaps
block new work; they never prove an order failed.

Successful closure authorizes only non-mutating PAPER/SHADOW operation. It is
not approval for live trading.
