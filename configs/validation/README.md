# Market-validation configuration

The checked-in `portfolio.paper.json`, `risk.paper.json`,
`mapping-selection.day0.json`, and `strategies-disabled.json` are the approved
M2 policy inputs. They do not authorize a session. `day1.example.json` and the
Day-0 evidence example deliberately contain placeholders and must not pass
unchanged.

Keep the current calendar, source approval, Zerodha dump-derived instrument
master/watchlist, runtime bundle, finalized authorization manifest, and
Telegram/session evidence under `.cache/market-validation`; this tree is
git-ignored. Never put credentials there.

The readiness command accepts only PAPER or SHADOW, validates calendar coverage,
requires one to four required NSE Zerodha quote mappings, decodes the existing
portfolio configuration, and requires the exact ten-rule Phase 3 production risk
catalog. `OPERATIONS_ONLY` permits the checked-in empty strategy list.
`FULL_PIPELINE` requires an enabled strategy classified
`PRODUCTION_CANDIDATE`; none currently exists, so Day 1 is
`STRATEGY_BLOCKED`.

No current repository fixture is suitable for Day 1. The checked-in market-data
fixture uses provider `fixture`, covers an expired date, and maps only a test
NIFTY instrument. Do not relabel it as Zerodha evidence.
