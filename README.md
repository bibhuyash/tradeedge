# TradeEdge

TradeEdge is a safety-first automated options-trading platform for the Indian market. The repository includes the Phase 0 runtime foundation and Phase 1 provider-neutral historical market-data foundation.

The application is **paper-only**. It contains no Zerodha network integration, live broker route, real credentials, trading strategy, or order-orchestration path.

## Prerequisites

- Go 1.23.4
- GNU Make (optional; the underlying Go commands can be run directly)

No third-party Go dependencies are required. Phase 0 uses `log/slog`, `net/http`, and other Go standard-library packages.

## Configuration

Configuration is loaded from environment variables. Copy `.env.example` only as a reference; the application does not automatically load dotenv files.

| Variable | Default | Description |
| --- | --- | --- |
| `TRADEEDGE_ENV` | `development` | Runtime environment label |
| `TRADEEDGE_HTTP_ADDR` | `:8080` | Health server listen address |
| `TRADEEDGE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |
| `TRADEEDGE_SHUTDOWN_TIMEOUT` | `10s` | Positive graceful-shutdown timeout |
| `TRADEEDGE_TRADING_MODE` | `paper` | Must be `paper`; every other value is rejected |

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
- `GET /readyz` returns readiness. Phase 0 becomes ready after local initialization and becomes unready before shutdown.

Example:

```sh
curl http://127.0.0.1:8080/healthz
curl http://127.0.0.1:8080/readyz
```

## Developer commands

```sh
make build
make test
make lint
make format
```

These run `go build ./...`, `go test ./...`, `go vet ./...`, and `gofmt` respectively.

## Historical market-data tool

Phase 1 provides an offline local-file tool. It never connects to Zerodha.

```sh
go run ./cmd/tradeedge-marketdata ingest \
  -master tests/testdata/marketdata/instrument-master.json \
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

## Architecture boundaries

- `internal/domain` owns typed values and shared domain contracts.
- `internal/instrumentmaster` separates canonical instrument identity from provider-token mappings.
- `internal/marketdata` validates, orders, stores, measures, and replays canonical quote and completed-candle events.
- Strategy code can receive only the canonical market-data event contract and has no broker capability.
- `internal/execution` owns the broker interface.
- `internal/adapters/broker/paper` is an in-memory, context-aware paper skeleton with duplicate prevention and no network access.
- Configuration, HTTP, and logging are platform concerns and do not contain trading policy.

Future execution orchestration must follow the documented sequence: strategy eligibility, portfolio allocation, central risk approval, execution, broker interaction, and reconciliation. No component may bypass that pipeline.

See `docs/` for the product, architecture, trading, reliability, integration, and phase plans.
