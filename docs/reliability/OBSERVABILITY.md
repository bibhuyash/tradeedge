# Observability

## Scope

TradeEdge uses structured logs, metrics, traces, health probes, and append-only audit events to explain operation and decisions.

## Assumptions

Telemetry systems may fail and must not become trading truth. Correlation identifiers connect a market event, signal, risk decision, intent, and broker order.

## Responsibilities

- Emit JSON logs with level, time, component, operation, and correlation context.
- Measure data freshness, decisions, rejections, order states, reconciliation, dependency latency, and readiness.
- Preserve material automated decisions as append-only audit events.

## Invariants

- Secrets, tokens, and sensitive account data are never logged.
- Audit records are not replaced by ordinary logs.
- Observability failure cannot silently permit unsafe trading.

## Failure Modes

Cardinality explosions, dropped telemetry, clock skew, redaction gaps, and alert fatigue reduce operational confidence.

## Trade-offs

Detailed evidence increases storage and operational cost but is required for diagnosis and readiness.

## Unresolved Questions

Metrics, tracing backends, retention, and alert ownership are selected before deployment.

## Acceptance Criteria

- Phase 0 emits structured startup and shutdown logs.
- Health and readiness are independently observable.
- Logging tests demonstrate structured output without secrets.
