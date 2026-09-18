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

1. Copy `.env.example` to the ignored `.env` and inject current credentials.
   When the previous Zerodha access-token session is absent or expired, place a
   fresh request token in that file and run the bounded bootstrap before
   retrieving the current instrument dump:

   ```powershell
   go run ./cmd/tradeedge-zerodha-auth authenticate -credentials-file .env
   ```

   It exchanges the request token at most once, atomically persists only the
   access token and expiry, and performs no instrument, REST-profile,
   WebSocket, order, or runtime operation. A valid persisted session is reused.
   Never copy credentials or command output containing them into evidence.
2. Generate the exact-date calendar and source manifest from the reviewed,
   checksum-verified NSE calendar policy with `generate-calendar`, then run
   `calendar-check`. A weekend, listed holiday, missing policy date, or source
   checksum mismatch fails closed.
3. Generate current bounded mappings from a current Zerodha instrument dump
   with `tradeedge-validation generate-shadow-derivatives`, providing accepted
   NIFTY and BANKNIFTY forward references, validity times, and all three output
   paths. Provider tokens are derived, never hand-edited.
4. Run `build-shadow-bundle` with the approved calendar, generated master and
   watchlist, `configs/validation/strategies-shadow.json`, portfolio/risk files,
   and both `qualification.*.shadow-collecting.json` files.
5. Run fresh Telegram evidence and Zerodha preflight for mode `SHADOW`. Pass
   `-credentials-file .env`; preflight reuses a valid restored access token or
   exchanges the request token exactly once, atomically persists only
   `TRADEEDGE_ZERODHA_ACCESS_TOKEN` and
   `TRADEEDGE_ZERODHA_ACCESS_TOKEN_EXPIRES_AT`, and uses that session for REST
   and WebSocket verification. It never prints or records the token.
6. Finalize a commit-, date-, artifact-, and evidence-bound SHADOW authorization
   with `tradeedge-validation authorize`. CI never issues this authorization.
7. Inspect the manifest and obtain explicit operator approval.
8. In PowerShell, remove any stale process-scoped manifest selector and validate
   the Compose configuration. Clearing the process value makes the manifest
   selector come from the current `.env`; quiet validation avoids printing the
   resolved secret-bearing environment:

   ```powershell
   Remove-Item Env:TRADEEDGE_AUTHORIZATION_MANIFEST_HOST -ErrorAction SilentlyContinue
   docker compose --env-file .env config --quiet
   ```

9. Only then start the canonical service:

   ```powershell
   docker compose --env-file .env up -d tradeedge-shadow
   ```

Confirm `/api/v1/integrations/zerodha/status` reports read-only SHADOW and
`/api/v1/shadow/runtime` reports broker orders disabled. Candidate warmup is not
a system failure. Stop on mapping conflict, checkpoint failure, unexpected order
frame, or authorization expiry.

The packet is session-specific. Do not create an authorization for a closed or
future session merely to complete preparation. On the actual trading date,
generate fresh date-bound artifacts. If Zerodha requires a new login, the sole
manual credential action is to place the resulting request token in the
untracked `.env`; preflight exchanges or reuses it and persists the restored
session fields without printing them.

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
