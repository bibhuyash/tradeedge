# ADR 0027: Authoritative Fill Ingestion and Position Reconciliation

## Status

Accepted for Phase 6 Milestone 2.

## Decision

Only immutable fills committed by the Phase 4 OMS may enter Phase 6
accounting. M2 validates fill, report, order, plan, intent, canonical instrument,
side, portfolio, and an immutable versioned one-to-one portfolio-to-account
binding. M1 position identity remains `(portfolio, instrument)`.

The M1 publication transaction is minimally extended with ingestion progress.
It commits the fill application, position revision, accounting checkpoint,
source checkpoint/checksum, and binding checksum together. Exact retries are
idempotent. Changed content, stale revisions, and canonical predecessors fail
closed. A predecessor is quarantined for verified isolated replay; M2 cannot
replace authoritative history.

Broker positions are immutable provider-neutral observations. Reconciliation
compares them with one stable local revision and publishes deterministic
evidence. Stale, unavailable, incomplete, or incomparable evidence is unknown
and blocks operations requiring broker certainty. Broker-only exposure is
critical. The reconciler has no accounting mutation or broker-order port.

PAPER observations compare only with paper accounting. Real SHADOW observations
are non-comparable evidence. OFFLINE and LIVE_DISABLED cannot become reconciled.

## Consequences

Accounting replay remains independent of broker observations. Account scope is
auditable without migrating M1 identity. Recovery can retry uncertain caller
attempts because only the atomic progress record proves commitment.

## Rejected Alternatives

Reports without fills, broker-position adoption, automatic cost-basis repair,
compensating orders, and opportunistic late-fill insertion bypass immutable
execution evidence or rewrite financially meaningful history.
