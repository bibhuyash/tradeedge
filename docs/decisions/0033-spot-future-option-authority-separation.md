# ADR 0033: Spot, future, and option authority separation

## Status

Accepted for Phase 8 M2 SHADOW/PAPER validation.

## Decision

NIFTY 50 is signal authority only. A uniquely resolved non-expired NIFTY
future supplies the forward reference used for option expiry and strike
selection. The selected option's own accepted quote is the sole authority for
PAPER fill, cost basis, valuation, and realized P&L.

M2 uses `nearest-eligible-nifty-future/v1`,
`nearest-option-expiry-min-one-day/v1`, and
`forward-atm-nearest-strike-half-up/v1`. It selects one long call from a
five-strike bounded neighborhood. BUY uses the option ask and SELL uses the
option bid. `option-touch-or-ltp-conservative/v1` permits an explicitly
labelled LTP approximation only when book levels are absent. IV and Greeks are
`UNAVAILABLE` and are never fabricated.

All contracts and provider mappings come from one checksum-versioned
instrument master. Missing, stale, ambiguous, expired, illiquid, or excessively
wide data fails closed. An open option is never migrated during rollover.
SHADOW emits decisions and notifications but no fill; PAPER alone may use the
deterministic simulator. LIVE remains unavailable.
