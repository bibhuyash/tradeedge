# Observability

## Scope

TradeEdge uses structured logs, metrics, traces, health probes, and append-only audit events to explain operation and decisions.

## Assumptions

Telemetry systems may fail and must not become trading truth. Correlation identifiers connect a market event, signal, risk decision, intent, and broker order.

## Responsibilities

- Emit JSON logs with level, time, component, operation, and correlation context.
- Measure data freshness, decisions, rejections, order states, reconciliation, dependency latency, and readiness.
- Count accepted, duplicate, late, malformed, missing, corrected, and stale market-data outcomes.
- Measure exchange-to-ingestion lag, reorder-buffer depth, dataset checksums, replay throughput, consumer time, scheduled waits, and pause duration.
- Preserve material automated decisions as append-only audit events.
- Count bounded strategy-runner starts, committed results, readiness blocks,
  duplicates, timeouts, panics, invalid outputs, conflicts, publication
  failures, duration, publication duration, state size, and in-flight work.

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
- Phase 1.1 exposes a typed recorder and a private Prometheus registry at `GET /metrics`.
- Health and readiness are independently observable.
- Logging tests demonstrate structured output without secrets.

## Phase 1.1 Metric Catalog

The catalog is `tradeedge_marketdata_{observations_total,quality_total,normalization_duration_seconds,transport_lag_seconds,event_age_seconds,reorder_buffer_depth,ready,readiness_transitions_total,coverage_ratio,missing_intervals_total,dataset_commits_total,dataset_commit_duration_seconds,dataset_bytes,checksum_failures_total,replay_events_total,replay_duration_seconds,replay_consumer_duration_seconds,replay_backpressure_seconds_total,replay_pause_seconds_total}`.

Labels are restricted to provider, exchange, segment, event kind, candle interval, quality/disposition/outcome, bounded readiness scope/state/reason, terminal state, and bounded watchlist ID. Instrument, event, dataset, replay, request, strategy, account, token, symbol, path, error, and free-text reason labels are prohibited.

Processing, lag/age, and long-operation histograms use the approved fixed bucket sets. Instrument IDs remain available in paginated diagnostics, not metric labels.

## Phase 2 Milestone 3 Metric Catalog

The adapter exposes
`tradeedge_strategy_{evaluations_total,evaluation_duration_seconds,publication_duration_seconds,state_bytes,in_flight}`.
Only stable definition ID and typed outcome label the counter and histograms.
Instance, frame, trigger, evaluation, proposal, instrument, configuration, and
state identities are prohibited labels. The runner imports only its
provider-neutral telemetry contract; Prometheus remains adapter-only.

The GET-only strategy API exposes bounded metadata for definitions, versions,
instances, checkpoints, recent evaluations, observations, advisory proposals,
and runner health under `/api/v1/strategy/`. Raw runtime state, configuration
payloads, arbitrary diagnostics, provider tokens, credentials, mutation,
activation, risk, and execution controls are not exposed. List limits default
to 50 and cannot exceed 100.

## Phase 3 Milestone 1

Milestone 1 adds no metrics, logs, traces, operational HTTP endpoints, or alert
rules. Risk evidence and portfolio-risk decisions are domain/audit contracts,
not Prometheus labels. A later milestone may expose bounded status and counters
but must not label metrics with portfolio, strategy, proposal, decision,
evaluation, allocation, instrument, configuration, account, error, or
free-text values.

## Phase 3 Milestone 3 Metric Catalog

The provider-neutral recorder is adapted to
`tradeedge_risk_{decisions_total,rule_results_total,evaluation_duration_seconds,publication_duration_seconds,in_flight}`.
Labels are restricted to registered rule ID and bounded outcome, status, effect,
and severity. Portfolio, strategy, proposal, decision, evaluation, allocation,
instrument, underlying, configuration, account, checksum, error, and free-text
values are prohibited labels.

The GET-only risk API under `/api/v1/risk/` exposes bounded snapshot, capital,
decision, violation, rule, control, configuration-metadata, and runner-health
views. Limits default to 50 and cannot exceed 100. It exposes no mutation,
credential, provider, broker, order, position, or execution capability.

## Phase 4 Milestone 3 Metric Catalog

Execution exposes `tradeedge_execution_{plans_total,submissions_total,order_events_total,reconciliation_total,reconciliation_issues_total,reconciliation_repairs_total,in_flight,unknown_orders,duration_seconds}` and `tradeedge_paper_broker_scenarios_total`.
Labels are restricted to finite operation, outcome, report/state, mismatch, and
paper-scenario vocabularies. Plan, order, client-order, broker, instrument,
account, payload, path, error, and free-text labels are prohibited.

The GET-only API under `/api/v1/execution/` exposes bounded recent lifecycle,
UNKNOWN, reconciliation, component-health, and audit status. Limits default to
50 and cannot exceed 100. Telemetry and audit status are diagnostic only and
cannot submit, cancel, reconcile, mutate OMS state, or establish trading truth.
