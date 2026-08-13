# ADR 0034: Shadow qualification evidence and scorecards

## Status

Accepted for Phase 8 M3.

## Decision

Real-market SHADOW observations are recorded by a provider-neutral qualification
engine that has no execution, OMS, accounting, or broker mutation dependency.
Its `ShadowQualificationPosition` is analytical evidence only. NIFTY and
BANKNIFTY use isolated series keyed by strategy, version, and underlying.

Signals, forward horizons, MFE/MAE, exits, and scorecard inputs are stored in an
atomic checksummed SHADOW checkpoint. Deterministic identifiers make retries
idempotent and conflicting or out-of-order evidence fails closed.

The scorecard uses integer minor units and basis points. Gross P&L is reported;
net P&L remains `NOT_AVAILABLE` until a versioned, sourced statutory cost
configuration is approved. Decision-time regime tags use only fixed-point EMA
separation and contemporaneous range ratios under a versioned transparent
policy; no future data or opaque confidence score is used.

`QUALIFIED` and `REJECTED` are explicit review transitions available only after
configurable sample gates. They grant no PAPER or LIVE capital authority.

## Consequences

Qualification evidence survives restart without becoming an authoritative
position. Missing observations remain typed unavailable evidence. The candidate
stays disabled and `NOT_ALPHA_QUALIFIED`; real orders remain unreachable.
