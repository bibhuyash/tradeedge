# Phase 6 Financial Release Gate

Run `go run ./cmd/tradeedge-phase6-gate` and retain its canonical JSON output
with the reviewed commit. `passed` must be true and `failure_reasons` empty.

Before closure, CI must pass formatting, full tests, Ubuntu `-race`, repeated
accounting/valuation/replay tests, vet, build, Phase 3/4/5 regressions, and the
boundary scans in `phase-6-accounting-release.yml`.

`PARTIAL`, `STALE`, or `UNAVAILABLE` financial readiness blocks risk decisions
requiring valuation. Operators inspect the bounded GET-only financial health,
snapshot, position, P&L, exposure, and readiness views. They must not repair
accounting from broker observations or substitute a candle/broker price.

Phase 6 closure is evidence for a non-live foundation. It does not authorize
`LIVE_ENABLED`, real broker mutation, or Phase 7 runtime activation.
