# Product Roadmap

## Scope

This roadmap sequences bounded milestones from foundation through independently approved live-readiness assessment.

```mermaid
flowchart LR
    P0["Phase 0: Foundation"] --> P1["Phase 1: Market Data"]
    P1 --> P2["Phase 2: Strategy & Evidence"]
    P2 --> P3["Phase 3: Portfolio & Risk"]
    P3 --> P4["Phase 4: Execution & OMS"]
    P4 --> P5["Phase 5: Broker Adapter & Reliability Validation"]
    P5 --> P6["Phase 6: Positions, P&L, and Reconciliation"]
    P6 --> G{"Explicit live approval?"}
    G -->|No| PT["Continue paper trading"]
    G -->|Yes| LP["Separately scoped live pilot"]
```

## Assumptions

Each phase has objective entry and exit criteria. Passing a technical milestone does not authorize live trading.

## Responsibilities

- Phase 0 establishes types, ports, configuration, observability foundations, and paper-only runtime.
- Phase 1 establishes reliable normalized market data.
- Phase 2 adds deterministic strategy evidence.
- Phase 3 establishes portfolio/risk decisions, bounded rule execution, atomic
  reservations, operational controls, and release evidence.
- Phase 4 establishes provider-neutral execution/OMS contracts before adding a
  deterministic paper broker, bounded operations, and machine-readable
  execution reliability evidence. Phase 4 closure remains paper-only.
- Phase 5 may design a Zerodha adapter behind the broker port and exercise it
  only through controlled paper integration, reliability validation, and
  failure drills; live orders remain separately approval-gated.
- Phase 5 closes with OFFLINE/PAPER/SHADOW observation and release evidence;
  LIVE_DISABLED remains blocked. Phase 6 begins authoritative positions, fills,
  P&L, and broker reconciliation without inheriting live-order authorization.
- Phase 6 Milestone 1 establishes immutable-fill-driven weighted-average
  positions and gross realized P&L. Fill ingestion and broker-position
  reconciliation remain Milestone 2.

## Invariants

- Only one major milestone is implemented at a time.
- Safety evidence cannot be waived to preserve schedule.
- Live enablement requires a separate design and approval.

## Failure Modes

Schedule pressure, incomplete evidence, flaky dependencies, or unresolved state must delay progression rather than reduce controls.

## Trade-offs

Sequential gates slow feature delivery but reduce coupled failure risk and make readiness evidence auditable.

## Unresolved Questions

- Numeric readiness thresholds and observation duration remain approval-gated.
- Disaster-recovery objectives must be set before production deployment.

## Acceptance Criteria

- Each phase has bounded deliverables and verification.
- Dependencies between phases are explicit.
- No roadmap phase silently enables real orders.
