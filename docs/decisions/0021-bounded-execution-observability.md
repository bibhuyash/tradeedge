# ADR 0021: Bounded Execution Observability and Read-Only Operations

## Status

Accepted for Phase 4 Milestone 3.

## Decision

Execution telemetry uses a provider-neutral recorder with a finite operation,
outcome, state, mismatch, and paper-scenario vocabulary. Prometheus labels may
contain only that vocabulary. Plan, order, client-order, broker, instrument,
account, payload, path, error, and free-text values are prohibited labels.

Operational HTTP under `/api/v1/execution/` is GET-only, uses a two-second
bounded read context, defaults to 50 records, and rejects limits above 100.
It exposes recent plans, orders, reports, fills, UNKNOWN orders, reconciliation,
coordinator, OMS, paper-broker, and bounded audit status. It has no control,
submission, cancellation, reconciliation-trigger, or state-mutation endpoint.

Health is observational and provider-neutral. UNKNOWN orders and critical
reconciliation evidence are `BLOCKED`; missing sources are `UNAVAILABLE`;
shutdown is `STOPPED`. Telemetry and the bounded in-memory journal are not
authoritative and cannot alter M1/M2 execution semantics.

## Consequences

TradeEdge identities may appear in bounded operator JSON but never as metric
labels. The diagnostic journal is process-local and intentionally not durable.

## Rejected Alternatives

Identity-labelled metrics have unbounded cardinality. POST-based operational
controls would create a second execution path. Telemetry-backed truth would
weaken the OMS and broker reconciliation boundaries.
