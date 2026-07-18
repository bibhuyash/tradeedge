# Data Flow

## Scope

This document defines the authoritative flow from external market data to reconciled state.

```mermaid
sequenceDiagram
    participant M as Market Data
    participant S as Strategy
    participant E as Eligibility
    participant P as Portfolio
    participant R as Risk
    participant X as Execution
    participant B as Broker
    participant C as Reconciler
    M->>S: Normalized event
    S->>E: Signal
    E->>P: Eligible signal
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

- Every order can be traced to market evidence, signal, allocation, and risk decision.
- Every ambiguous outcome routes to reconciliation.
- There is no direct strategy-to-broker path.
