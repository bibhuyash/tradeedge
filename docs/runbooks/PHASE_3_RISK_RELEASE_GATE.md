# Phase 3 Portfolio-Risk Release Gate

## Scope

This gate closes Phase 3 with deterministic production-style portfolio-risk controls, bounded read-only operations, race/stress evidence, and explicit proof that no broker or execution capability was introduced. It does not authorize paper execution or Phase 4 implementation.

## Workflow

Run the `Phase 3 Portfolio Risk Release` workflow against the reviewed commit. A cancelled, failed, or infrastructure-interrupted run is not release evidence.

The authoritative artifact is `phase-3-milestone-3-risk-release.json` with its adjacent SHA-256 file. It records the commit and workflow identity, ten-rule catalog, deterministic catalog result, ordinary/race/repeated-test results, concurrency/resource bounds, forbidden-capability scan, validation result, and explicit failure reasons.

## Release conditions

- Formatting, ordinary tests, full race tests, ten repeated risk/replay/release-gate race runs, vet, and build pass.
- Rule boundary, modification, unknown-input, control-state, containment, atomicity, duplicate/conflict, replay, and concurrency tests pass.
- No authoritative floating-point arithmetic, credentials, broker, Zerodha, live-trading, order, or execution-intent capability appears in Phase 3 code.
- JSON validation and SHA-256 generation succeed, every failure-reason list is empty, and the uploaded commit SHA matches the reviewed head.

Phase 3 remains open if any condition fails. Evidence never authorizes live trading.
