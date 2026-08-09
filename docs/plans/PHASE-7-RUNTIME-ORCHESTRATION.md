# Phase 7: Trading Runtime Orchestration

## Scope

Milestone 1 composes the released Phase 1-6 contracts into one deterministic,
non-mutating PAPER/SHADOW runtime. It adds no strategy, broker mutation,
Telegram integration, database migration, or live mode.

## Milestone 1 Checklist

- [x] Typed runtime and exchange-session state machines with fail-closed transitions.
- [x] Versioned calendar schema for NORMAL, PRE_CAS, CAS_ACTIVE, and POST_CAS.
- [x] Explicit LTP, CAS indicative/reference/equilibrium, and official-close provenance.
- [x] Mode-specific required/optional readiness aggregation.
- [x] Restore-before-activate startup and checksummed cross-subsystem manifest.
- [x] Registered, disabled, warming, active, session/risk-restricted, halted, and stopping strategy states.
- [x] CAS policy and centrally classified risk-reducing exception boundary.
- [x] Bounded synchronous stage handoff and admission backpressure.
- [x] PAPER and existing single-book SHADOW composition contracts.
- [x] Fill-to-accounting-to-financial-state-to-risk feedback boundary.
- [x] Bounded drain, cancellation, shutdown, health, and GET-only status.
- [x] Deterministic replay/checkpoint and Phase 7 release evidence workflow.

## Safety Boundaries

Only PAPER and SHADOW can run the pipeline. OFFLINE and LIVE_DISABLED fail
closed. SHADOW translates and fingerprints hypothetical requests before using
the deterministic paper broker; real broker positions are non-comparable
observation evidence and cannot repair the hypothetical TradeEdge book.

Checkpoint manifests reference verified subsystem heads rather than replacing
their atomic publications. Unknown execution, corrupt lineage, accounting
failure, unavailable risk authority, or required readiness loss stops new
exposure at the smallest safe scope.

## Closure

M1 closes only when formatting, full tests, full race, repeated runtime/replay
and Phase 2-6 regressions, vet, build, capability scans, and checksummed release
evidence pass. Closure does not authorize live trading or Phase 7 M2.

