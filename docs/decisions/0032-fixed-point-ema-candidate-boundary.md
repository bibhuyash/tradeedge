# ADR 0032: Fixed-point EMA candidate and explicit execution mapping

## Status

Accepted for Phase 8 M1 PAPER validation.

## Context

TradeEdge needs one explainable production-candidate strategy. NIFTY 50 is an
approved observation series but is not tradable. WebSocket arrival order is
not deterministic, and binary floating point is not authoritative for trading
decisions.

## Decision

Use one EMA20/EMA50 crossover definition over canonical completed one-minute
NIFTY 50 candles. EMA values use price minor units scaled by 1,000,000. Each
recurrence applies alpha `2/(period+1)` and rounds half away from zero. The
first canonical close in the bounded 64-candle frame seeds each EMA; at least
50 samples are required. The bounded window is part of calculation policy v1.

Crossovers are edge-triggered. A bullish crossover emits a BUY proposal and a
bearish crossover emits a SELL exit proposal. The proposal leg always names an
explicitly configured, separately priced tradable PAPER instrument. Missing or
invalid mapping fails closed. No option-selection policy is implied.

The checked-in candidate is disabled, one-lot, one-position-intent,
`NORMAL_TRADING` only, and CAS-restricted. Phase 3 retains all risk authority.

## Consequences

Replays are stable across platforms and do not depend on tick arrival order.
The fixed scale bounds precision and makes rounding observable. The candidate
cannot run a full session until a reviewed execution mapping and canonical
execution-price series are checksum-authorized. This delay is intentional.

## Rejected alternatives

- Direct index execution: the instrument is observation-only.
- Dynamic option selection: it introduces another strategy and mapping policy.
- `float64` EMA: it is unsuitable for authoritative deterministic decisions.
- Reusing the moving-average fixture: it is classified non-production and uses
  a simple moving average rather than the approved calculation policy.
