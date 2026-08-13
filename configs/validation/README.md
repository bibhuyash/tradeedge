# Market-validation configuration

The checked-in `portfolio.paper.json`, `risk.paper.json`,
`mapping-selection.day0.json`, and `strategies-disabled.json` are the reviewed
M2 policy inputs. They do not authorize a session. `day1.example.json` and the
Day-0 evidence example deliberately contain placeholders and must not pass
unchanged.

The `qualification.*.shadow.json` files are disabled Phase 8 M3 policy
fixtures. They isolate NIFTY and BANKNIFTY and grant no PAPER, LIVE, or broker
mutation authority.

Keep the current calendar, source approval, Zerodha dump-derived instrument
master/watchlist, runtime bundle, checksum-bound Telegram and Zerodha preflight
evidence, finalized authorization manifest, and
Telegram/session evidence under `.cache/market-validation`; this tree is
git-ignored. Never put credentials there.

The readiness command accepts only PAPER or SHADOW, validates calendar coverage,
requires one to twelve bounded NSE Zerodha quote mappings, decodes the existing
portfolio configuration, and requires the exact ten-rule Phase 3 production risk
catalog. `OPERATIONS_ONLY` permits the checked-in empty strategy list.
`FULL_PIPELINE` requires an enabled strategy classified
`PRODUCTION_CANDIDATE`. The one checked-in Phase 8 candidate is deliberately
disabled and its configured PAPER execution identity is not present in the
Day-0 observation-only instrument master, so Day 1 remains `STRATEGY_BLOCKED`.

No current real-market mapping is suitable for Day 1 execution. The checked-in market-data
fixture uses provider `fixture`, covers an expired date, and maps only a test
NIFTY instrument. Do not relabel it as Zerodha evidence.

`derivatives-disabled.json` is the Phase 8 M2 activation-ready policy, not an
authorization. It binds the spot/future/option separation and bounded long-call
selection policies while remaining disabled. Current futures and option tokens
must always be regenerated from a fresh checksum-pinned Zerodha dump.

`tradeedge-validation close-day0` finalizes a checksum-bound operations-only
closure from the authorization, external evidence, runtime attestation,
container log, clean checkpoint, and operator controls. It preserves the
distinction between a partial session and a full-session validation; neither
classification grants trading authority.
