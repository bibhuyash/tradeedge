# Phase 8 M1 PAPER Strategy Validation

## Preconditions

- Keep the checked-in candidate disabled for ordinary and Day-0 operations.
- Use PAPER mode only. `LIVE_ENABLED` is not a valid runtime mode.
- Prepare a reviewed instrument master containing NIFTY 50 observation data
  and the exact configured tradable execution instrument plus its canonical
  one-minute price series.
- Bind configuration, dataset, calendar, instrument master, portfolio policy,
  and Phase 3 risk policy checksums into the session authorization.

## Fail-closed checks

Do not enable the candidate if the execution mapping is absent, expired, or
different from the configured identity; if position state is unavailable or
disagrees; if readiness is stale; during CAS; outside `NORMAL_TRADING`; while
STOP_NEW_EXPOSURE or a risk circuit is active; or after an unclean restore.

Expected non-actions are visible as `INSUFFICIENT_HISTORY`,
`STALE_MARKET_DATA`, `SESSION_NOT_ALLOWED`, `CAS_RESTRICTED`,
`POSITION_ALREADY_OPEN`, `COOLDOWN_ACTIVE`, `NO_CROSSOVER`,
`EXECUTION_MAPPING_UNAVAILABLE`, or `AUTHORITATIVE_STATE_CONFLICT`.

## Verification

Run the focused package repeatedly, then the repository gates:

```text
go test ./internal/strategy/ema -count=20
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

Inspect `/api/v1/strategy/validation` with GET only. Confirm one-lot proposals,
edge-triggered IDs, Phase 3 decisions, PAPER broker fills, authoritative
positions and P&L, EOD drain, restart equivalence, and zero Zerodha mutation.

## Incident response

On mismatch, stale data, duplicate identity with changed content, panic,
timeout, unknown execution state, or accounting disagreement, block new
exposure, preserve the evidence, drain if safe, and follow the relevant Phase
1-7 recovery runbook. Never retry an ambiguous broker submission blindly.
