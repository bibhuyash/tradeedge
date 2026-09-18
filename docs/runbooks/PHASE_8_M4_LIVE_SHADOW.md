# Phase 8 M4 live read-only SHADOW

This workflow prepares an operator-approved real-market SHADOW session. It does
not authorize PAPER execution or LIVE trading. The service must produce zero
orders, fills, paper mutations, and real broker mutations.

## Modes

- `OFFLINE`: no real-market integration.
- `PAPER`: a separately authorized simulated-execution composition.
- `SHADOW`: real read-only observations, `EMA_REFERENCE_V1`, bounded derivatives
  selection, released Phase 3 risk, qualification, Telegram, and scorecards.
- `LIVE`: unavailable.

PAPER authorization never authorizes SHADOW. SHADOW authorization has scope
`QUALIFICATION_ONLY` and states that paper execution and real broker mutation
are prohibited.

## Pre-session preparation

Control Plane V2 owns authentication. `.env` contains the static API key and
secret plus the existing non-secret artifact selectors; it must not contain a
request token or access token. Start the localhost-only control plane:

```powershell
docker compose --env-file .env up -d tradeedge-control
```

Read `http://127.0.0.1:8081/api/v1/session/status`. If the state is
`LOGIN_REQUIRED` or `EXPIRED`, get the URL from
`http://127.0.0.1:8081/api/v1/session/login-url`, complete browser login, and
submit the returned request token exactly once:

```powershell
$body = @{ request_token = Read-Host 'Zerodha request token' } | ConvertTo-Json
Invoke-RestMethod -Method Post -ContentType 'application/json' -Body $body `
  -Uri 'http://127.0.0.1:8081/api/v1/session/exchange'
$body = $null
```

Do not save the request token in shell history, `.env`, a runtime bundle or
evidence. The response must be `AUTHENTICATED`. Restarting the control plane
must preserve `AUTHENTICATED` with `reused=true`; no second exchange occurs.
`ERROR` is fail-closed and requires inspection of the local session file or
provider availability rather than another blind submission.

The current checksum-pinned runtime bundle and `QUALIFICATION_ONLY`
authorization manifest remain mandatory prerequisites. Their generation is a
separate reviewed workflow and is not performed by Control Plane V2. Do not use
the legacy `tradeedge-prepare -> tradeedge-zerodha-auth` stdout protocol as an
authentication step for this V2 workflow.

From a clean candidate commit, generate them with the persisted V2 session:

```powershell
docker compose --env-file .env run --rm tradeedge-prepare
```

Preparation loads `/var/lib/tradeedge/session/zerodha.json` read-only and calls
the shared instrument-snapshot and preflight implementation in-process. It
cannot request login, consume a request token, or exchange a token.

Before creating the candidate commit, the same real V2 bridge can be exercised
without producing authorization or persistent release artifacts:

```powershell
docker compose --env-file .env run --rm -e TRADEEDGE_PREPARATION_ACCEPTANCE_ONLY=true tradeedge-prepare
```

This acceptance-only mode permits a dirty tree, stores its intermediate files
in a temporary directory that is removed on exit, and stops before authorization
generation and artifact-selector persistence. Normal preparation continues to
require a clean tree.

Only after `READY`, start the canonical service:

```powershell
docker compose --env-file .env up -d tradeedge-shadow
```

Confirm `/api/v1/integrations/zerodha/status` reports read-only SHADOW and
`/api/v1/shadow/runtime` reports broker orders disabled. Candidate warmup is not
a system failure. Stop on mapping conflict, checkpoint failure, unexpected order
frame, or authorization expiry.

The authorization packet remains session-specific. Do not create one for a
closed or future session merely to complete preparation. Browser login and the
single localhost exchange request above are the only manual authentication
actions.

## Session 1 record and Session 2 target

Session 1 on 2026-08-13 is complete as `PARTIAL_SESSION`. NIFTY and BANKNIFTY
each produced 361 accepted observations and six completed one-minute candles.
The candle pipeline passed and EMA reached 6/50; six candles were expected
because live capture began near market close. There were no SHADOW proposals,
broker orders, paper mutations, or real broker mutations, and the candidate is
still `NOT_ALPHA_QUALIFIED`.

The duplicate shutdown checkpoint publication seen after Session 1 was fixed by
commit `3d1ef202d927ee16bb1d6a562a0301900beb7e3f`. `RunWithOptions` owns shutdown
and publishes one final checkpoint. Do not treat the historical Session 1
closure fields as a current defect or rewrite its evidence to a later commit.

For Session 2, start sufficiently early to collect at least 50 completed
one-minute candles independently for NIFTY and BANKNIFTY. Expected progression
is market data ready, candle aggregation, EMA 50/50, strategy ready,
`SHADOW_COLLECTING`, then a genuine signal if the market supplies one. A genuine
signal must exercise derivatives selection, released Phase 3 risk,
qualification evidence, and outbound Telegram while producing zero broker
orders. `NO_ACTION` or `NO_CROSSOVER` is a valid result; never alter EMA20/EMA50
to manufacture a signal.

At EOD require the session scorecard and real-market evidence, one clean
checkpoint publication, clean shutdown, and process exit code 0. PAPER and LIVE
remain disabled and real broker mutation remains unreachable.

## Shutdown ownership

The application lifecycle in `RunWithOptions` is the sole owner of production
SHADOW shutdown. It invokes the registered SHADOW trading runtime, which stops
the read-only stream and session, closes local controls, publishes one clean
final checkpoint, and drains risk resources. Repeated or concurrent shutdown
requests converge on that same operation and receive its original result.

An EOD close finalizes the qualification session but does not create a second
shutdown owner. The subsequent process shutdown persists the already-closed
state once. A real checkpoint revision conflict or persistence failure remains
fatal and must not be ignored or retried as a blind idempotent write.

## Evidence

Engineering tests never create real-market evidence. After an actual authorized
session is cleanly closed, the operator may use `finalize-shadow-session` to
create `evidence/real-market/YYYY-MM-DD/shadow-session.json`. The command rejects
open sessions, non-SHADOW mode, absent authorization/connection evidence, or any
paper or broker mutation count.
