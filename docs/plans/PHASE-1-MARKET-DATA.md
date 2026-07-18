# Phase 1: Market Data

## Scope

Add reliable Zerodha market-data ingestion, instrument normalization, freshness checks, reconnect behavior, replay fixtures, and health signals. This document does not authorize Phase 1 implementation during Phase 0.

## Assumptions

Provider details will be verified against current official Zerodha documentation before coding. Market data is unreliable and may contain gaps or reordering.

## Responsibilities

- Load and version the instrument reference set.
- Normalize streaming events into domain contracts.
- Detect stale, duplicate, out-of-order, and missing data.
- Reconnect with bounded backoff and expose degraded readiness.
- Produce deterministic recorded fixtures within licensing constraints.

## Invariants

- Stale or uncertain data cannot produce tradable input.
- Provider SDK types remain inside the adapter.
- No order placement or strategy implementation is introduced.

## Failure Modes

Authentication expiry, disconnects, silent stream stalls, reference-data drift, bursts, and clock disagreement must be observable and fail closed.

## Trade-offs

Persisting all raw ticks improves replay but increases cost and licensing risk; retention requires an explicit decision.

## Unresolved Questions

Instrument universe, candle construction, raw-data retention, licensing, and freshness thresholds require approval.

## Acceptance Criteria

- Recorded and live-equivalent paths produce the same normalized contracts.
- Gap and stale-data tests are deterministic.
- Provider failure changes health/readiness without enabling trading.
