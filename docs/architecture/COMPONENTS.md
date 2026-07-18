# Components

## Scope

This document assigns ownership and dependency direction inside the modular monolith.

```mermaid
flowchart LR
    MD["Market Data"] --> ST["Strategy"]
    ST --> EL["Eligibility"]
    EL --> PA["Portfolio Allocation"]
    PA --> RE["Risk Engine"]
    RE --> EX["Execution"]
    EX --> BA["Broker Adapter"]
    BA --> RC["Reconciliation"]
    RC --> AC["Positions & P&L"]
    AU["Audit"] --- ST
    AU --- RE
    AU --- EX
```

## Assumptions

Interfaces are small and owned by their consumers. Cross-module shared values are strongly typed domain contracts.

## Responsibilities

- Market data normalizes provider input.
- The instrument master owns canonical instruments and time-bounded provider-token mappings.
- Market-data ingestion validates, deduplicates, reorders, records quality, and writes immutable datasets.
- Replay reads canonical datasets and invokes consumers synchronously in deterministic order.
- Strategy emits signals only.
- Lifecycle establishes eligibility.
- Portfolio allocates constrained capital.
- Risk approves or rejects proposed exposure.
- Execution owns order state and broker access.
- Reconciliation repairs internal understanding from broker facts.
- Notifications report but never determine trading truth.

## Invariants

- Dependencies flow toward domain policy, never from domain to adapters.
- Provider tokens never serve as canonical instrument identifiers.
- Only validated canonical quote and completed-candle events may cross into consumers.
- Allocation and risk precede submission.
- Audit records accompany material decisions.

## Failure Modes

Missing modules, circular dependencies, bypass imports, or untyped shared values can erode safety boundaries.

## Trade-offs

Additional packages and contracts create ceremony but make unsafe coupling visible during review.

## Unresolved Questions

- Exact persistence repositories will be designed with PostgreSQL.
- Accounting and lifecycle policy APIs will be finalized in their implementation phases.

## Acceptance Criteria

- Every requested port has one clear owner.
- Strategy code has no broker capability.
- Paper and future live adapters satisfy the same execution-owned contract.
