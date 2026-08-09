# Market Validation Day 1

## Status and authority

This runbook is for read-only real-market observations through PAPER/SHADOW.
It is not Phase 8 and never authorizes live trading.

The repository is currently **NOT READY** for Day 1. The default application
does not compose the Phase 7 runtime or real Zerodha connectivity/stream
dependencies. PAPER/SHADOW startup therefore cannot pass the readiness command.
Do not substitute OFFLINE, disabled market data, fixtures, manual JSON, or a
Telegram `READY` state for the missing runtime evidence.

Current blockers are:

1. `cmd/tradeedge` does not compose the released Phase 1-7 pipeline.
2. PAPER/SHADOW Zerodha integration is constructed with empty dependencies.
3. No production Zerodha WebSocket connector is present.
4. No approved current calendar, Zerodha mapping, watchlist, paper-capital
   amount, portfolio policy, or risk policy is present.
5. No production-candidate strategy exists. Full-pipeline validation is
   unavailable; do not enable the moving-average engineering fixture.
6. Risk, execution, financial, and Phase 7 runtime operations are not mounted
   by the default command.
7. Authoritative subsystem restoration/checkpoint evidence is not durable in
   the default in-memory composition.
8. No operator control exists to stop new exposure or explicitly close an EOD
   session. `Ctrl+C` performs bounded process shutdown only.

## Approved modes and secrets

Only `OFFLINE`, `PAPER`, `SHADOW`, and `LIVE_DISABLED` exist. Day 1 begins in
PAPER. `TRADEEDGE_TRADING_MODE` remains `paper` in both PAPER and SHADOW.

For read-only Zerodha operation, inject these names through the approved secret
mechanism; never put their values in a file, evidence, log, metric, command
argument, or ticket:

- `TRADEEDGE_ZERODHA_API_KEY`
- `TRADEEDGE_ZERODHA_API_SECRET`
- `TRADEEDGE_ZERODHA_REQUEST_TOKEN`, when performing the one-time exchange
- `TRADEEDGE_ZERODHA_ACCESS_TOKEN`, when supplying a previously exchanged token
- `TRADEEDGE_ZERODHA_ACCESS_TOKEN_EXPIRES_AT`
- `TRADEEDGE_TELEGRAM_BOT_TOKEN`
- `TRADEEDGE_TELEGRAM_CHAT_ID`

PAPER/SHADOW also require `TRADEEDGE_ZERODHA_READ_ONLY=true`. A missing,
expired, stale, or inconsistent input fails closed.

## Required operator configuration

Prepare reviewed files outside tracked source under
`.cache/market-validation/config/`:

- Calendar covering the exact NSE date in `Asia/Kolkata`.
- Same-day canonical instrument master with provider `zerodha` mappings.
- Watchlist containing one to four required NSE quote streams.
- Strategy file. Use `configs/validation/strategies-disabled.json` for
  `OPERATIONS_ONLY` sessions.
- Existing Phase 3 portfolio configuration with an approved paper-capital base.
- Exact ten-rule production risk configuration.
- A session readiness file derived from
  `configs/validation/day1.example.json`.

There is currently no repository-supported real Zerodha watchlist because no
valid Zerodha mapping is committed. Do not use the expired test fixture. Once
mappings are supplied, approve NIFTY and BANKNIFTY index quotes first; add no
option merely to create trades.

Use this conservative policy after approving a paper-capital base:

- 80% reserve plus 10% emergency reserve.
- 10% maximum total open/new exposure and per-strategy allocation.
- 5% maximum instrument exposure; 10% underlying/exposure-group exposure.
- 0.5% daily-loss limit and 1% drawdown circuit threshold.
- One active production-candidate strategy, one lot per order, and two
  simultaneous positions at most.
- Stale financial state, UNKNOWN, reconciliation mismatch, CAS restriction, or
  an active control blocks new exposure.

## Evidence structure

Use the git-ignored layout below. Paths stored inside daily records are relative
to the session directory and must never name credentials or tokens.

```text
.cache/market-validation/
  config/
  records/
  YYYY-MM-DD/
    readiness.json
    telegram-check.json
    raw/
      runtime.json
      market-data.json
      strategies.json
      risk.json
      execution.json
      financial.json
      notifications.json
      cas.json
      eod.json
      checkpoint.json
```

Evidence files are create-once. A command refuses to overwrite different
content at an existing path.

## Start of day

Run from the repository root in PowerShell. These commands do not start a
market session by themselves.

```powershell
git status --short
git rev-parse HEAD
$env:GOCACHE='D:\Projects\tradeedge\.cache\go-build'
go test ./...
go vet ./...
go build ./...
```

Require an empty status and put the exact commit in the reviewed Day-1 JSON.
Create the evidence directory and copy only the non-secret template:

```powershell
New-Item -ItemType Directory -Force -Path '.cache\market-validation\YYYY-MM-DD' | Out-Null
Copy-Item -LiteralPath 'configs\validation\day1.example.json' -Destination '.cache\market-validation\YYYY-MM-DD\day1.json'
```

Edit the copy with the approved date, commit, portfolio ID, scope, and file
paths. Do not put credentials in it.

After securely injecting Telegram variables, send the fixed non-trading test:

```powershell
go run ./cmd/tradeedge-validation telegram-check -date YYYY-MM-DD -mode PAPER -output '.cache\market-validation\YYYY-MM-DD\telegram-check.json'
```

After the runtime-composition blockers are closed, set non-secret mode values
and start the existing application in a dedicated terminal:

```powershell
$env:TRADEEDGE_TRADING_MODE='paper'
$env:TRADEEDGE_ZERODHA_MODE='PAPER'
$env:TRADEEDGE_ZERODHA_READ_ONLY='true'
$env:TRADEEDGE_MARKETDATA_CALENDAR='.cache\market-validation\config\calendar.json'
go run ./cmd/tradeedge
```

In the operator terminal, execute the single fail-closed gate:

```powershell
go run ./cmd/tradeedge-validation readiness -config '.cache\market-validation\YYYY-MM-DD\day1.json' -output '.cache\market-validation\YYYY-MM-DD\readiness.json' -repo .
```

It requires the approved commit/configuration, evidence directory, current
calendar/mappings, real market-data readiness, restored Phase 7 runtime,
read-only Zerodha session/stream, risk catalog, inactive kill switch, closed
circuit breaker, healthy OMS/reconciliation, complete financial state, and
Telegram health/delivery evidence. Any failed check means **do not begin**.

## During market

Use bounded GET-only views; do not rely on continuous log watching:

```powershell
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/runtime/status'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/market-data/readiness'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/market-data/readiness/instruments'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/strategy/runner'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/execution/health'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/integrations/zerodha/health'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/notifications/health'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/notifications/failures'
```

Risk and financial requests require the approved portfolio ID:

```powershell
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/risk/kill-switch?portfolio=PORTFOLIO_ID'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/risk/circuit-breaker?portfolio=PORTFOLIO_ID'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/risk/decisions?portfolio=PORTFOLIO_ID'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/financial/readiness?portfolio_id=PORTFOLIO_ID'
```

On readiness loss, UNKNOWN, mismatch, stale valuation, missing mapping, or
incorrect session state, stop admitting new exposure through the existing
runtime safety gate. Never resubmit an UNKNOWN or repair a cursor manually.

## CAS

At PRE_CAS, CAS_ACTIVE, and POST_CAS, capture runtime, market-data, strategy,
risk, positions/financial, and CAS evidence. Verify calendar-driven transitions
and that non-`CAS_SAFE` strategies cannot add exposure.

```powershell
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/runtime/status'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/operations/cas-evidence?limit=100'
```

Keep LTP, CAS indicative/reference/equilibrium, and official close provenance
distinct. Missing CAS-specific prices remain explicitly unavailable. Do not
add CAS trading logic or substitute them for canonical-LTP valuation.

## End of day

The absence of an operator stop-new-exposure/EOD command is a current blocker.
Do not claim a complete session until that existing-domain control is composed.
Once available, the order is: stop new exposure, resolve UNKNOWN, reconcile,
final valuation, CAS evidence, EOD report, incident review, drain, checkpoint,
then clean shutdown.

Before shutdown, capture:

```powershell
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/operations/eod/latest'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/execution/unknown'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/execution/reconciliation'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/notifications/failures'
```

Stop the application with `Ctrl+C`; require the bounded shutdown to complete.
Prepare a draft from `configs/validation/day-record.example.json`, reference
the captured files and their SHA-256 values, then finalize it:

```powershell
go run ./cmd/tradeedge-validation finalize-day -input '.cache\market-validation\YYYY-MM-DD\day-draft.json' -output '.cache\market-validation\records\YYYY-MM-DD.PAPER.day.json'
```

The command derives `VALID`, `VALID_WITH_INCIDENTS`, or `INVALID`. It preserves
an INVALID record and exits non-zero. Mandatory-evidence absence, a positive-PnL
invalid day, UNKNOWN, mismatch, mutation, bad checkpoint, insufficient
readiness, or incomplete required CAS/financial evidence cannot become VALID.

Update the 10-20 session scorecard:

```powershell
go run ./cmd/tradeedge-validation scorecard -records '.cache\market-validation\records' -output '.cache\market-validation\scorecard.json'
```

Create a new scorecard output name when content changes because evidence is
create-once.

## Incident response

- **Market data/session/mapping:** block new exposure, retain the failed state,
  and inspect readiness diagnostics. Do not infer session truth from weekdays.
- **UNKNOWN/reconciliation/accounting:** halt the affected scope, preserve exact
  evidence, and reconcile. Never retry based only on a timeout.
- **Kill switch/circuit breaker:** treat the control as authoritative; no manual
  threshold relaxation is permitted.
- **Telegram:** inspect notification health/failures and continue only if local
  operational evidence remains complete. Telegram never affects authority.
- **Checkpoint/evidence corruption:** mark the session INVALID and restart only
  from a verified manifest.
- **Any broker mutation or live-mode evidence:** halt immediately, mark INVALID,
  preserve evidence, and escalate. No market-validation procedure can waive it.
