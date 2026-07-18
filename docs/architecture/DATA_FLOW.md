# Data Flow

## Scope

This document defines the authoritative flow from external market data to reconciled state.

```mermaid
sequenceDiagram
    participant P as Provider Fixture
    participant N as Normalize and Validate
    participant D as Dataset
    participant M as Replay / Live Contract
    participant S as Strategy
    participant E as Eligibility
    participant P as Portfolio
    participant R as Risk
    participant X as Execution
    participant B as Broker
    participant C as Reconciler
    P->>N: Provider observation
    N->>D: Ordered canonical event
    D->>M: Deterministic replay
    M->>S: Validated canonical event
    S->>E: Advisory trade proposal
    E->>P: Eligible proposal
    P->>R: Proposed allocation
    R->>X: Approved intent
    X->>B: Idempotent submission
    B-->>X: Result or ambiguity
    X->>C: Reconciliation request
    C->>B: Authoritative lookup
    C-->>X: Resolved state
```

## Assumptions

Events carry timestamps and correlation identifiers. Linked legs share an execution-intent identifier.

## Responsibilities

Each stage validates its inputs, records its decision, and passes an explicit typed result to the next stage.

## Invariants

- A rejected or missing decision halts the flow.
- Malformed, duplicate, or too-late market observations never enter the canonical consumer stream.
- Equal exchange timestamps are ordered by provider sequence when available, then event ID.
- Timeouts are ambiguous, not failures.
- Reconciliation precedes retry when submission outcome is unknown.
- Alerts never substitute for persisted truth.

## Failure Modes

Stale or out-of-order market data, duplicate signals, allocation races, expired risk decisions, partial legs, and lost responses are contained and audited.

## Trade-offs

Synchronous control flow is simpler initially but may add latency. Messaging is deferred until measured load or isolation demands it.

## Unresolved Questions

- Event-retention and replay formats will be fixed during market-data design.
- Maximum decision age will be risk-policy configuration.

## Acceptance Criteria

- Every order can be traced to market evidence, evaluation, advisory proposal,
  allocation, and risk decision.
- Every future order can be traced to a stable evaluation and advisory proposal,
  followed by separate eligibility, allocation, and risk decisions.
- Every ambiguous outcome routes to reconciliation.
- There is no direct strategy-to-broker path.

## Phase 1.1 Operational Flow

```mermaid
flowchart LR
    C["Verified versioned calendar"] --> E["Expected windows"]
    S["Fixture source"] --> N["Normalize and validate"]
    N --> O["Deduplicate and bounded reorder"]
    E --> G["Calendar-aware gap detector"]
    O --> G
    G --> D["Immutable dataset revision"]
    D --> V["Checksum verification"]
    V --> P["Append-only publication generation"]
    P --> R["Serial deterministic replay"]
    O --> F["Freshness and readiness evaluator"]
    E --> F
    F --> H["readyz and operational diagnostics"]
```

Only a complete verified dataset can be published. Publication uses an expected-current ID; rollback appends a generation. Live-equivalent consumers still receive the same canonical event contract and synchronous backpressure remains explicit.

## Phase 2 Milestone 1 Evaluation Boundary

```mermaid
flowchart LR
    C["Canonical completed candles"] --> F["Immutable synchronized frame"]
    R["READY evidence"] --> F
    V["Definition version + canonical configuration"] --> E["Deterministic evaluation"]
    S["Prior immutable runtime state"] --> E
    F --> E
    E --> N["NO_ACTION"]
    E --> O["OBSERVATION"]
    E --> T["Advisory TRADE_PROPOSAL"]
    E --> NS["Next immutable runtime state"]
    T -. "future only" .-> EL["Eligibility, allocation, and central risk"]
```

Single-stream definitions use a one-series frame. Multi-instrument definitions
declare either exact-close synchronization or latest-completed input semantics.
Missing, stale, or non-ready required data prevents construction of a valid
evaluation context. Output publication, checkpointing, runner isolation, and
proposal persistence remain later Phase 2 milestones.
