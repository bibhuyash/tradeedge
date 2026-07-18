# ADR 0002: Versioned Calendar and Market-Data Readiness

## Status

Accepted for Phase 1.1 hardening. The thresholds are test defaults, not production trading approval.

## Scope

Define how TradeEdge decides whether an NSE stream is expected and whether normalized market data is operationally usable.

## Assumptions

Exchange calendars are corrected over time, provider delivery and exchange timestamps can fail independently, and absence of events is meaningful only when an explicit calendar says an event is expected.

## Decision and Responsibilities

- Use explicit, versioned `Asia/Kolkata` calendar fixtures with one entry for every covered civil date.
- Represent holidays, modified hours, special sessions, and breaks directly; never infer them from weekdays.
- Evaluate required and optional streams at instrument, provider, watchlist, and global levels.
- Evaluate exchange age, ingestion age, and transport lag independently.
- Permit future strategy evaluation only for global `READY`.
- Treat `DISABLED` and `SESSION_CLOSED` as operationally ready but never trading-permitted.

## Invariants

`UNKNOWN`, `INCOMPLETE`, `NO_DATA`, and `STALE` fail closed. Missing calendar coverage is `CALENDAR_OUT_OF_RANGE`, not a holiday. A missing expected candle overrides a subsequently closed session.

## Failure Modes

Missing or invalid coverage, overlapping sessions, clock skew, provider unavailability, stale timestamps, and missing expected windows produce stable reason codes and HTTP 503 readiness where applicable.

## Trade-offs

Explicit daily fixtures require an authoritative update process, but eliminate unsafe weekday and wall-clock guesses. Bounded watchlists limit metric cardinality and diagnostic response size.

## Unresolved Questions

The authoritative production calendar source, correction SLA, production watchlist, and freshness thresholds require CEO approval.

## Acceptance Criteria

Tests cover holidays, breaks, modified sessions, missing coverage, warm-up, independent age failures, optional streams, and closed-session behavior. No state other than `READY` sets `trading_permitted=true`.
