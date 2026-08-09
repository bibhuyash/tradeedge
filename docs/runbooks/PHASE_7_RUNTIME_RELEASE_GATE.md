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

