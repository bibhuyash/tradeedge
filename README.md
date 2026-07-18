# TradeEdge

TradeEdge is a safety-first automated options-trading platform for the Indian market. The repository includes the Phase 0 runtime foundation, Phase 1 provider-neutral historical market data, Phase 1.1 operational hardening, and the first bounded Phase 2 strategy-contract milestone.

The application is **paper-only**. It contains no Zerodha network integration, live broker route, real credentials, trading strategy, or order-orchestration path.

## Prerequisites

- Go 1.23.4
- GNU Make (optional; the underlying Go commands can be run directly)

The market-data domain remains dependency-light. The official Prometheus Go client v1.23.2 is the only direct third-party Go dependency; imports are confined to the Prometheus adapter and HTTP composition.

## Configuration

Configuration is loaded from environment variables. Copy `.env.example` only as a reference; the application does not automatically load dotenv files.

| Variable | Default | Description |
| --- | --- | --- |
| `TRADEEDGE_ENV` | `development` | Runtime environment label |
| `TRADEEDGE_HTTP_ADDR` | `:8080` | Health server listen address |
| `TRADEEDGE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |
| `TRADEEDGE_SHUTDOWN_TIMEOUT` | `10s` | Positive graceful-shutdown timeout |
| `TRADEEDGE_TRADING_MODE` | `paper` | Must be `paper`; every other value is rejected |
| `TRADEEDGE_MARKETDATA_CALENDAR` | empty | Optional verified calendar fixture for the read-only calendar API |
| `TRADEEDGE_MARKETDATA_DATASET_ROOT` | empty | Optional local immutable dataset repository for read-only dataset APIs |

Do not add broker tokens, API secrets, or account credentials to repository files.

## Run locally

PowerShell:

```powershell
$env:TRADEEDGE_HTTP_ADDR = "127.0.0.1:8080"
$env:TRADEEDGE_TRADING_MODE = "paper"
go run ./cmd/tradeedge
```

POSIX shell:

```sh
TRADEEDGE_HTTP_ADDR=127.0.0.1:8080 \
TRADEEDGE_TRADING_MODE=paper \
go run ./cmd/tradeedge
```

Stop with `Ctrl+C`. The process withdraws readiness and performs a bounded graceful shutdown.

## Operational endpoints

- `GET /healthz` returns liveness.
- `GET /readyz` returns process and market-data readiness, stable reasons, calendar version, and `trading_permitted`.
- `GET /metrics` exposes the private Prometheus registry.
- `GET /api/v1/market-data/readiness` returns global/provider/watchlist state.
- `GET /api/v1/market-data/readiness/instruments` returns filtered, paginated diagnostics (maximum 250).
- `GET /api/v1/market-data/quality` returns aggregate missing ranges.
- `GET /api/v1/market-data/calendar?exchange=NSE&date=YYYY-MM-DD` returns explicit session truth.
- `GET /api/v1/market-data/datasets/{id}` and `/lineage` return verified metadata.
- `GET /api/v1/market-data/datasets/current?series=name` returns the highest valid publication generation.

All operational endpoints are GET-only. With no watchlist configured, `/readyz` remains operationally ready with market state `DISABLED` and `trading_permitted=false`.

Example:

```sh
curl http://127.0.0.1:8080/healthz
curl http://127.0.0.1:8080/readyz
curl http://127.0.0.1:8080/metrics
```

## Developer commands

```sh
make build
make test
make lint
make format
```

These run `go build ./...`, `go test ./...`, `go vet ./...`, and `gofmt` respectively.

## GitHub Actions

The repository has three safety-scoped workflows:

- **CI** runs formatting verification, race-enabled tests, `go vet`, and a
  complete build for pull requests and pushes to `main`.
- **Delivery** runs the same verification and packages the two Linux AMD64
  commands with SHA-256 checksums for `v*` tags or an explicit manual run.
- **Phase 1.1 market-data release gate** is manual and always runs ordinary
  verification, race-enabled tests, every classification/load profile, and a
  non-shortenable 30-minute real-time soak. It uploads machine-readable evidence
  and fails if evidence generation or upload fails.

Delivery produces a short-lived GitHub Actions artifact. It does not create a
GitHub Release, deploy an environment, access credentials, connect to Zerodha,
or enable live trading. Production deployment remains blocked until hosting,
static outbound IP, secret storage, approval gates, rollback, and kill-switch
requirements are approved.

Third-party actions are pinned to immutable commit SHAs. Dependabot checks
monthly for GitHub Actions updates so those pins can be advanced through normal
review and CI.

Recommended `main` branch protection should require the CI job named
`Format, test, vet, and build`, require pull-request review, and prohibit
force-pushes. Configure these controls in GitHub after the workflows are pushed.

## Historical market-data tool

Phase 1 provides an offline local-file tool. It never connects to Zerodha.

```sh
go run ./cmd/tradeedge-marketdata ingest \
  -master tests/testdata/marketdata/instrument-master.json \
  -calendar tests/testdata/marketdata/calendar.json \
  -input tests/testdata/marketdata/observations.ndjson \
  -root .cache/datasets

go run ./cmd/tradeedge-marketdata verify \
  -root .cache/datasets \
  -dataset <dataset-id>

go run ./cmd/tradeedge-marketdata replay \
  -root .cache/datasets \
  -dataset <dataset-id> \
  -speed max
```

Replay speed accepts `max`, `1x`, or a positive integer acceleration such as `10x`. Replay invokes consumers serially and uses synchronous backpressure.

Corrections and publication:

```sh
go run ./cmd/tradeedge-marketdata rebuild \
  -master tests/testdata/marketdata/instrument-master.json \
  -calendar tests/testdata/marketdata/calendar.json \
  -input tests/testdata/marketdata/observations-corrected.ndjson \
  -root .cache/datasets -parent <current-id> -series nse-quotes \
  -reason "official source correction" -request-id correction-001

go run ./cmd/tradeedge-marketdata publish \
  -root .cache/datasets -series nse-quotes -dataset <child-id> \
  -expected-current <current-id> -reason "verified correction" -request-id publication-001

go run ./cmd/tradeedge-marketdata rollback \
  -root .cache/datasets -series nse-quotes -dataset <earlier-id> \
  -expected-current <current-id> -reason "rollback failed correction" -request-id rollback-001

go run ./cmd/tradeedge-marketdata lineage \
  -root .cache/datasets -dataset <dataset-id> -series nse-quotes
```

Repeated correction/publication requests use stable request IDs. A stale expected-current ID fails rather than overwriting another operator’s publication.

Load verification:

```sh
go run ./cmd/tradeedge-marketdata loadtest -profile=normal
go run ./cmd/tradeedge-marketdata loadtest -profile=burst
```

The `soak` profile intentionally runs for 30 real minutes. Trigger the complete
release gate with:

```sh
gh workflow run marketdata-load.yml --ref <branch-or-commit>
```

Only the manual Ubuntu workflow is approval evidence: it verifies a working C
compiler, runs `go test -race ./...`, applies bounded heap/goroutine/cancellation
tolerances, reconciles every generated and downstream event, and retains the
reports for 90 days. See
`docs/runbooks/MARKET_DATA_LOAD_TESTING.md` for the evidence contract.

## Architecture boundaries

- `internal/domain` owns typed values and shared domain contracts.
- `internal/instrumentmaster` separates canonical instrument identity from provider-token mappings.
- `internal/marketdata` validates, orders, stores, measures, and replays canonical quote and completed-candle events.
- `internal/marketdata/calendar` and `readiness` make expectation and freshness explicit.
- `internal/marketdata/storage` plus the file adapter preserve revisions and append-only publication history.
- `internal/marketdata/telemetry` owns metric semantics; the Prometheus library remains in its adapter.
- `internal/strategy/model` owns stable strategy versions, canonical
  configuration and state, subscriptions, immutable candle frames, evidence,
  evaluation results, and advisory proposals.
- `internal/strategy` exposes a broker-neutral deterministic definition
  contract. A definition receives no broker, risk, allocation, account, order,
  or position capability.
- `internal/execution` owns the broker interface.
- `internal/adapters/broker/paper` is an in-memory, context-aware paper skeleton with duplicate prevention and no network access.
- Configuration, HTTP, and logging are platform concerns and do not contain trading policy.

Future execution orchestration must follow the documented sequence: strategy eligibility, portfolio allocation, central risk approval, execution, broker interaction, and reconciliation. No component may bypass that pipeline.

## Phase 2 status

Only Phase 2 Milestone 1 is implemented. It supplies deterministic domain
contracts; it does not register or run strategies. There is no strategy runner,
checkpoint repository, proposal publication, reference trading strategy,
automatic lifecycle transition, backtester, risk decision, allocation, or order
execution in this milestone.

Trade proposals are advisory. They contain stable provenance, integer reference
prices, normalized leg ratios, bounded validity, evidence, and a
`STRATEGY_BUDGET_BPS` sizing intent. They deliberately contain no broker token,
account ID, executable quantity, broker order, or risk approval.

See `docs/` for the product, architecture, trading, reliability, integration, and phase plans.
