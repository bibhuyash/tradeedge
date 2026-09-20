# TradeEdge Research V1

## Purpose and safety boundary

Research V1 is a deterministic, historical, offline simulation environment. It exists to test hypotheses and plumbing; it does not authorize a strategy for PAPER or LIVE use, and its output is not evidence of alpha.

The execution modes are deliberately distinct:

- **RESEARCH** replays versioned historical files through an in-memory simulated portfolio.
- **SHADOW** consumes real-time market data and records hypothetical internal execution without broker mutation.
- **PAPER** uses the production domain pipeline with a paper broker adapter. It remains disabled.
- **LIVE** permits real broker mutation only after separate readiness and authorization. It remains disabled.

Research packages have no dependency on broker ports, Zerodha adapters, authentication, order submission, or the trading runtime. The research command reads local JSON files and writes a JSON report; it has no credential or network configuration.

`EMA_REFERENCE_V1` is a pipeline/control strategy. It is not alpha-qualified and must not be promoted based on either runtime or research output. `CONTROL_STRATEGY_V1` is also only an engine fixture and deliberately has no profitability claim.

## Point-in-time contract

Dataset schema `tradeedge.research.dataset/v1` contains a dataset version, normalized instruments, and observations. Instrument metadata carries `available_from`; an observation cannot predate that value. Observations carry exchange time, last price, optional bid and ask, volume, and optional open interest. Prices are integer minor units.

Loading is fail-closed. Unknown JSON fields, invalid timestamps or option metadata, negative values, crossed books, duplicate events, unknown instruments, and observations before metadata availability are rejected. Accepted observations are replayed by exchange timestamp and stable observation identity.

A feature frame at T is built from a point-in-time view containing only metadata and observations available at or before T. Strategy code never receives the unfiltered dataset. Orders emitted at T can fill only from a strictly later observation.

## Fill, cost, and accounting policy

The baseline fill policy is `tradeedge.research.fill/v1`:

- BUY uses the valid ask; SELL uses the valid bid.
- Missing required book data rejects the fill.
- Configured slippage is always adverse and rounded upward.
- Spread impact and slippage are recorded separately.

Fee schedules use `tradeedge.research.cost/v1`. Every component is a versioned integer rational with an explicit base and rounding rule. TradeEdge does not embed or claim current Indian brokerage, STT, exchange, GST, stamp-duty, or regulatory rates. Test rates are explicitly synthetic.

The engine owns cash, pending orders, positions, fills, closed trades, and P&L. Duplicate fills, same-time fills, impossible exits, quantity underflow, and arithmetic overflow fail closed. Positions still open at dataset end remain open and are valued at the final point-in-time last price; no liquidation is fabricated.

## Offline harness

Run:

```text
go run ./cmd/tradeedge-research -dataset historical.json -config research-config.json
```

The run configuration schema is `tradeedge.research.run/v1`. It must select `CONTROL_STRATEGY_V1`, engine policy `tradeedge.research.engine/v1`, fill policy `tradeedge.research.fill/v1`, starting capital, integer quantity, direction, slippage basis points, and a complete versioned cost configuration.

Output is canonical JSON using UTC RFC3339 nanosecond timestamps, integer money/statistics, sorted collections, and no creation time. Identical dataset and configuration produce byte-equivalent reports. Profit factor is an exact reduced rational and is explicitly unavailable when there are no losing trades. Sharpe and Sortino are intentionally absent because no sampling definition is established in M1.

## M2 historical dataset qualification

M2 adds a data-only pipeline:

```text
Raw CSV -> normalization -> point-in-time instrument resolution
        -> calendar and quality validation -> canonical dataset
        -> deterministic manifest -> HistoricalSource -> backtest
```

The provider-neutral local JSON artifact binds normalized content to its
source, explicit calendar version, instrument-master checksum, and semantic
configuration. Identical inputs produce the same identity; changed data,
calendar, instrument metadata, or semantic configuration changes it. Storage
is behind an interface. M2 does not include or download a real dataset.

The CSV adapter uses RFC3339 exchange timestamps and integer minor-unit prices.
Bid, ask, volume, and open interest are optional and blank values remain
absent. Historical instrument metadata owns expiry, strike, option type, lot
size, and availability intervals; current specifications are never inferred.
The calendar explicitly lists trading/holiday dates and regular, modified, or
exceptional sessions. Observations do not create sessions.

Futures remain individual contracts. A continuous reference requires a named,
versioned rollover policy and records its source contract at each rollover;
without one, resolution fails. Option-chain snapshots expose only metadata and
observations available at the requested instant. ATM calculation uses the
supplied point-in-time reference and deterministic lower-strike tie breaking;
it never chooses a trade.

Bars support deterministic 1-, 5-, and 15-minute intervals. Each interval is
`[StartTime, EndTime)`. Volume is summed only when supplied and the last
supplied open interest is retained. A bar becomes available at `EndTime`, never
before it.

Quality findings carry `INFO`, `WARNING`, `ERROR`, or `FATAL`; policy determines
whether a dataset is `QUALIFIED`, `DEGRADED`, or `REJECTED`. Findings cover
duplicates, ordering, timestamps, prices, crossed markets, gaps, unknown
instruments, option metadata, post-expiry and outside-session records, missing
references, and explicitly cumulative fields. Ordinary OI may rise or fall and
is checked for monotonicity only when source semantics declare it cumulative.

**BAD DATA CAN CREATE FAKE ALPHA.** Survivorship and lookahead can expose
contracts or prices not knowable at decision time. Point-in-time contract
specifications and expiry metadata matter because lot sizes and universes
change. Naively stitched futures move history across rollover boundaries.
Invented bid/ask values create unavailable fills. A final OHLC value exposed
before bar close leaks the future. Qualification is a prerequisite for
research, not evidence of alpha.

```text
tradeedge-research dataset import --input historical.csv --instrument-master instruments.csv --calendar calendar.json --output dataset.json --source SOURCE
tradeedge-research dataset validate --dataset dataset.json
tradeedge-research dataset inspect --dataset dataset.json
```

Commands print `DATASET_VERSION`, `OBSERVATIONS`, `TRADING_DAYS`, and `QUALITY`.
The importer has no network, credentials, broker, order, SHADOW, PAPER, or LIVE
capability.
