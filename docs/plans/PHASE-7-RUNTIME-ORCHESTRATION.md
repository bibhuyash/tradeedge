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
evidence pass. M1 closure alone does not authorize live trading or M2.

## Milestone 2 Checklist

- [x] Provider-neutral immutable operational events and deterministic identities.
- [x] Bounded asynchronous dispatcher, retries, rate limits, suppression, and failure evidence.
- [x] Optional outbound-only Telegram adapter and redacted configuration.
- [x] Independent structured CAS evidence and deterministic EOD reporting.
- [x] Replay delivery suppression, bounded telemetry, and GET-only operational APIs.
- [ ] Phase 7 M2 Ubuntu race/stress workflow and checksummed evidence pass.

M2 does not authorize live trading, Telegram commands, or a CAS strategy. M3
trading-day closure and failure drills remain separate.

## Milestone 3 Checklist

- [x] Fail-closed Phase 7 closure schema and command.
- [x] Deterministic PAPER/SHADOW full-day scenario inventory.
- [x] Market-data, strategy, risk, OMS, accounting, valuation, broker,
  Telegram, CAS, and runtime failure-drill inventory with blast radii.
- [x] Restart-boundary, Telegram-isolation, CAS-safety, replay, and
  non-mutation enforcement fields.
- [x] Same-commit Phase 1-7 regression, race, stress, security, and artifact
  workflow.
- [x] PAPER/SHADOW operating and evidence-preservation runbook.
- [ ] Reviewed-commit Ubuntu closure workflow and checksummed artifact pass.

M3 is closed only by the final workflow artifact. Local harness output without
workflow identity and every external gate fails closed. Closure authorizes
continued PAPER/SHADOW validation only; it never authorizes live trading.
