# Market-validation configuration

These files are fail-closed templates, not an approved market-day
configuration. `day1.example.json` deliberately contains impossible placeholder
identities and must not pass readiness unchanged.

Keep session-specific calendar, Zerodha instrument master, watchlist, portfolio,
risk, and Telegram-check evidence under `.cache/market-validation`; this tree is
git-ignored. Never put credentials there.

The readiness command accepts only PAPER or SHADOW, validates calendar coverage,
requires one to four required NSE Zerodha quote mappings, decodes the existing
portfolio configuration, and requires the exact ten-rule Phase 3 production risk
catalog. `OPERATIONS_ONLY` permits the checked-in empty strategy list.
`FULL_PIPELINE` requires an enabled strategy classified
`PRODUCTION_CANDIDATE`; none currently exists.

No current repository fixture is suitable for Day 1. The checked-in market-data
fixture uses provider `fixture`, covers an expired date, and maps only a test
NIFTY instrument. Do not relabel it as Zerodha evidence.
