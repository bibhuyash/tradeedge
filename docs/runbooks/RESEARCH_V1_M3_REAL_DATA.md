# Research V1 M3 Real Historical Data

## Safety and licensing

Use only data that the operator is licensed to retain and analyze. Keep the raw
bundle and generated artifacts below `.cache/research-data/`; that directory is
already excluded by the repository's `.cache/` rule. Never put API tokens,
credentials, or account data in a bundle. This workflow is offline and has no
broker, SHADOW, PAPER, LIVE, or order capability.

No real dataset is shipped with TradeEdge. Fixtures do not count as real data.
Until a licensed external bundle passes these steps, report
`REAL_DATASET_IMPORTED=NO` and `READY_FOR_REAL_DATA_IMPORT=YES`.

## Bundle contract

The JSON bundle schema is `tradeedge.research.mapped-csv-bundle/v1`. It must
declare:

- `provider`, optional `source_version`, `data_class` set to
  `EXTERNAL_MARKET_DATA`, and an RFC3339 `acquired_at`.
- `market`, requested start/end dates, `interval` (`1m`, `5m`, or `15m`),
  `timezone` set to `Asia/Kolkata`, and timestamp semantics `START` or `END`.
- `price_unit` set to `MINOR`; prices must already be integer paise.
- `bars`, `instruments`, and `calendar` file references. Each reference has a
  relative `path`, stable provider `source_id`, and lowercase SHA-256.
- Column names for instrument identifier, timestamp, OHLC, and optional volume
  and open interest.

The instrument CSV uses the M2 point-in-time columns plus a required
`source_instrument_id`. Required M2 columns are `symbol`, `underlying`,
`exchange`, `segment`, `instrument_type`, `tick_size_minor`, `currency`, and
`available_from`. Futures also supply expiry and historical lot/tick metadata.
Do not populate these values from a current instrument master unless the
historical validity interval has been independently verified.

Bar timestamps must be RFC3339 with an explicit offset. Optional volume or OI
is represented by an empty field, never zero-filled. The calendar uses
`tradeedge.research.calendar/v1` and explicitly lists trading and non-trading
days and any modified or special sessions.

M3 qualification requires bar coverage for both the INDEX and FUTURE types of
NIFTY and BANKNIFTY. Options remain optional. Absence of any required series is
an error and produces `REJECTED` rather than a misleading partial acceptance.

## Import and qualification

From the repository root:

```text
go run ./cmd/tradeedge-research dataset import --adapter mapped-csv --bundle .cache/research-data/bundle.json --output .cache/research-data/dataset.json
go run ./cmd/tradeedge-research dataset validate --dataset .cache/research-data/dataset.json --output .cache/research-data/quality.json
go run ./cmd/tradeedge-research dataset inspect --dataset .cache/research-data/dataset.json
```

Import is create-once and will not overwrite an artifact. A correction requires
new raw bytes, checksums, and a new output path. Retain the rejected artifact as
evidence; do not edit its quality status.

`inspect` reports dataset/source identity, the real-data marker, underlyings,
actual range, interval, session and bar counts, contracts, missing bars,
duplicates, quality status, and raw/normalized checksums. Reconcile these
counts with the provider export before declaring acceptance.

## Acceptance report

Report `REAL_DATASET_IMPORTED=YES` only for a licensed external artifact whose
recomputed status is `RESEARCH_READY`. Populate the milestone response directly
from `inspect` and the quality JSON. Options may remain absent. PAPER and LIVE
remain disabled and real broker mutations remain unreachable.

If no real files were supplied, use:

```text
OUTCOME=TRADEEDGE_RESEARCH_V1_M3
STATUS=PARTIAL
REAL_DATA_SOURCE=NONE
REAL_DATASET_IMPORTED=NO
QUALITY_STATUS=NOT_RUN
BROKER_DEPENDENCY=NONE
REAL_BROKER_ORDERS=0
PAPER=DISABLED
LIVE=DISABLED
REAL_BROKER_MUTATION=UNREACHABLE
BLOCKER=Licensed real NIFTY and BANKNIFTY historical bundle not supplied
READY_FOR_RESEARCH_V1_M4=NO
READY_FOR_REAL_DATA_IMPORT=YES
```
