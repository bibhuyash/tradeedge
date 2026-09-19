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
