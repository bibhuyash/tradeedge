# Phase 8 M2 NIFTY derivatives SHADOW/PAPER validation

The candidate remains disabled. This runbook grants no session authority.

1. Obtain the current Zerodha instrument dump through the approved read-only
   workflow and record its SHA-256. Never edit a checksum.
2. Uniquely resolve NIFTY 50, the nearest eligible NIFTY future, and the bounded
   option universe. Reject expired, ambiguous, or unmapped contracts.
3. Verify spot, future, and selected-option readiness independently. The option
   quote must meet freshness, spread, and liquidity policy.
4. Run SHADOW first. Confirm notifications say SHADOW/PAPER VALIDATION and that
   no fill, position, or broker mutation exists.
5. PAPER may simulate one long-call lot after central risk. Verify option-touch
   fill, position, valuation, reversal/EOD exit, P&L, replay, and restart.
6. UNKNOWN execution, CAS transition, checksum change, stale data, rollover
   ambiguity, notification incident, or checkpoint mismatch blocks new work.
   Never migrate an open option automatically.
7. Activation requires exact-commit CI, fresh bundle and preflight, Telegram
   acceptance, a new authorization manifest, and CEO/operator approval.

IV and Greeks are `UNAVAILABLE`. The EMA reference candidate is not
alpha-qualified.
