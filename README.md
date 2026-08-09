# TradeEdge

TradeEdge is a safety-first automated options-trading platform for the Indian market. The repository includes the Phase 0 runtime foundation, released Phase 1 market data, Phase 2 strategy, Phase 3 portfolio/risk, Phase 4 paper execution/OMS, and the Phase 5 M3 non-mutating Zerodha PAPER/SHADOW integration.

The application is **paper-only**. The default runtime contains no composed
Zerodha network route, live broker route, real credentials, or production
trading strategy. The M1 Zerodha package exposes only authentication, profile,
and instrument reads and has no order mutation API. The moving-average crossover is a non-production
engineering fixture with no profitability claim.

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
| `TRADEEDGE_STRATEGY_MAX_CONCURRENCY` | `4` | Bounded concurrent evaluations across different instances; valid range 1–64 |
| `TRADEEDGE_STRATEGY_TIMEOUT` | `100ms` | Cooperative per-evaluation deadline; positive and at most one minute |
| `TRADEEDGE_RISK_MAX_CONCURRENCY` | `4` | Bounded concurrent portfolio evaluations; valid range 1–64 |
| `TRADEEDGE_RISK_TIMEOUT` | `100ms` | Cooperative per-decision deadline; positive and at most one minute |

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
- `GET /api/v1/strategy/definitions` and `/versions?definition=id` return bounded registry metadata.
- `GET /api/v1/strategy/instances` returns bounded instance metadata.
- `GET /api/v1/strategy/checkpoints?instance=id` returns checksums, revisions, schemas, and state sizes, never raw state.
- `GET /api/v1/strategy/evaluations?instance=id`, `/observations`, and `/proposals` return bounded decision summaries.
- `GET /api/v1/strategy/runner` returns runner health and recent typed failure summaries.

Strategy list endpoints default to 50 records and reject limits above 100. All
operational endpoints are GET-only. With no watchlist configured, `/readyz`
remains operationally ready with market state `DISABLED` and
`trading_permitted=false`.

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

## Market-validation evidence tool

`tradeedge-validation` performs fail-closed Day-1 checks and produces immutable
daily records and 10-20 session scorecards. It does not start a runtime, enable
live trading, or grant order authority. The checked-in Day-1 configuration is a
placeholder and intentionally cannot pass. See
`docs/runbooks/MARKET_VALIDATION_DAY_1.md` before using it.

```sh
go run ./cmd/tradeedge-validation readiness -config <approved-day1.json> -output <evidence>/readiness.json -repo .
go run ./cmd/tradeedge-validation finalize-day -input <draft.json> -output <records>/YYYY-MM-DD.MODE.day.json
go run ./cmd/tradeedge-validation scorecard -records <records> -output <evidence>/scorecard.json
```

## GitHub Actions

The repository has four safety-scoped workflows:

- **CI** runs formatting verification, race-enabled tests, `go vet`, and a
  complete build for pull requests and pushes to `main`.
- **Delivery** runs the same verification and packages the three Linux AMD64
  commands with SHA-256 checksums for `v*` tags or an explicit manual run.
- **Phase 1.1 market-data release gate** is manual and always runs ordinary
  verification, race-enabled tests, every classification/load profile, and a
  non-shortenable 30-minute real-time soak. It uploads machine-readable evidence
  and fails if evidence generation or upload fails.
- **Phase 2 strategy-runner stress** runs race-enabled strategy runner,
  repository, fixture, and replay tests ten times. It uploads a machine-readable
  summary without repeating the 30-minute market-data soak.

Delivery packages the application, historical market-data tool, and
market-validation evidence tool in a short-lived GitHub Actions artifact. It
does not create a GitHub Release, deploy an environment, access credentials,
connect to Zerodha, or enable live trading. Production deployment remains blocked until hosting,
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
- `internal/strategy/storage` owns provider-neutral registry, checkpoint,
  evaluation, observation, proposal, restoration, and atomic-publication
  contracts.
- `internal/adapters/strategy/memory` provides the bounded, concurrency-safe
  reference repository. It uses optimistic state revisions and one immutable
  snapshot swap for all-or-nothing publication.
- `internal/strategy/runner` owns readiness gating, deterministic trigger and
  evaluation identity, per-instance serialization, bounded cross-instance
  concurrency, cooperative deadlines, panic containment, and atomic
  publication.
- `internal/strategy/replay` turns completed replay candles into the same
  immutable frame contract and invokes consumers synchronously.
- `internal/strategy/telemetry` owns provider-neutral runner measurements;
  Prometheus remains adapter-only.
- `internal/strategy/fixtures/movingaverage` is explicitly classified
  `NON_PRODUCTION_ENGINEERING_FIXTURE`.
- `internal/portfolio/model` owns immutable capital, exposure, strategy
  allocation, allocation-candidate, kill-switch, circuit-breaker, snapshot,
  revision, and deterministic identity contracts.
- `internal/risk/model` owns versioned policy, pure rule input/result, typed
  evidence, violation, aggregate evaluation, and non-executable
  `PortfolioRiskDecision` contracts.
- `internal/portfolio/config` and `internal/risk/config` accept bounded
  canonical integer-only JSON. Duplicate keys, floats, unknown fixed fields,
  invalid limits, and unknown or duplicate rule identities fail closed.
- Portfolio and risk storage contracts are provider-neutral. The bounded M2
  reference adapter atomically commits decision artifacts, an optional capital
  reservation, and portfolio checkpoint/revision under optimistic revision
  control.
- `internal/execution/model` owns Phase 4 M1 provider-neutral execution
  authority, plan, leg, order, state-machine, report, fill, and stable identity
  contracts.
- `internal/execution/storage` owns optimistic revisions, checksummed
  checkpoints, restoration, and atomic order/report/fill publication.
- `internal/adapters/execution/memory` is the bounded concurrency-safe M1 OMS
  reference store. It is not durable production storage.
- The Phase 0 `internal/execution` broker interface remains uncomposed pending
  its replacement by the Milestone 2 provider-neutral broker port.
- `internal/adapters/broker/paper` is an in-memory, context-aware paper skeleton with duplicate prevention and no network access.
- Configuration, HTTP, and logging are platform concerns and do not contain trading policy.

Future execution orchestration must follow the documented sequence: strategy eligibility, portfolio allocation, central risk approval, execution, broker interaction, and reconciliation. No component may bypass that pipeline.

## Phase 2 status

Phase 2 Milestones 1–3 are implemented. They supply deterministic domain
contracts, provider-neutral repositories, checksummed checkpoint restoration,
atomic in-memory publication, and a bounded synchronous runner. The runner
checks lifecycle and Phase 1.1 readiness before invoking strategy code, permits
only one evaluation per instance, limits cross-instance work to four by
default, derives stable identities, contains strategy panics, and applies a
100 ms cooperative deadline.

There is no automatic lifecycle transition, production strategy, backtester,
risk decision, allocation, executable quantity, order, position, or broker
execution. The runner never retries strategy code after a revision conflict.
Timeout enforcement is cooperative: strategy implementations must observe
`context.Context`; TradeEdge does not create an unbounded goroutine in an
attempt to kill non-cooperative Go code.

Trade proposals are advisory. They contain stable provenance, integer reference
prices, normalized leg ratios, bounded validity, evidence, and a
`STRATEGY_BUDGET_BPS` sizing intent. They deliberately contain no broker token,
account ID, executable quantity, broker order, or risk approval.

Each strategy instance begins with revision-zero state. An evaluation based on
revision `N` may publish only checkpoint `N+1`. The checkpoint, evaluation
record, and optional observation or proposal become visible through one atomic
snapshot swap. Exact retries are idempotent; stale revisions, changed payloads
under an existing identity, checksum mismatch, cancellation, or injected
storage failure publish nothing.

### Phase 2 closure evidence

The manual Ubuntu release gate runs ordinary checks, the complete Go race
suite, ten independent race-enabled strategy stress repetitions, and the
deterministic closure harness:

```sh
gh workflow run strategy-runner-stress.yml --ref <branch-or-commit>
```

Its authoritative artifact is
`phase-2-milestone-3-strategy-stress.json`, accompanied by a SHA-256 file and
the underlying logs. The workflow fails on concurrent same-instance execution,
cross-instance limit violations, duplicate publication, result loss, failed
containment, replay divergence, resource growth beyond explicit tolerances, or
artifact failure. See
`docs/runbooks/PHASE_2_STRATEGY_RELEASE_GATE.md` for the evidence contract.

## Phase 3 status

Milestone 1 defines deterministic portfolio snapshots, capital accounting,
current/incremental/projected exposure, strategy allocation state,
allocation candidates, risk policies, pure rule contracts, typed evidence and
violations, aggregate evaluations, and `APPROVED`, `MODIFIED`, `REJECTED`, and
`DEFERRED` portfolio-risk decisions.

Decision validation binds proposal, snapshot revision, allocation candidate,
policy version, configuration hash, and evaluation outcome. All approved
capital, leg bounds, constraints, and validity are canonical identity-bearing
content; APPROVED authority equals its candidate and MODIFIED authority is a
strict subset.

Milestone 2 adds a synchronous bounded runner, deterministic allocation and
ordered rule orchestration, atomic decision/checkpoint publication, optimistic
revision enforcement, duplicate suppression, recovery checkpoints, and serial
replay. An allocation candidate remains non-authoritative before commit and an
approved decision remains non-executable. No production rule catalog,
operational API, Prometheus wiring, broker integration, credential, order, or
live-trading capability exists. See `docs/plans/PHASE-3-PORTFOLIO-RISK.md`.

See `docs/` for the product, architecture, trading, reliability, integration, and phase plans.

## Phase 5 Milestone 3 status

Milestone 3 adds explicit OFFLINE/PAPER/SHADOW/LIVE_DISABLED modes,
quote-observed deterministic paper fills, exact would-be request capture,
bounded stream supervision, aggregate readiness, GET-only diagnostics, and
machine-readable release evidence. Runtime composition contains no reachable
Zerodha order mutation and no unrestricted live mode. See
`docs/plans/PHASE-5-ZERODHA.md`.
