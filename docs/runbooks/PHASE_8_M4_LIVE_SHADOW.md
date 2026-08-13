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
8. Only then start the canonical service:

   ```powershell
   docker compose --env-file .env up -d tradeedge-shadow
   ```

Confirm `/api/v1/integrations/zerodha/status` reports read-only SHADOW and
`/api/v1/shadow/runtime` reports broker orders disabled. Candidate warmup is not
a system failure. Stop on mapping conflict, checkpoint failure, unexpected order
frame, or authorization expiry.

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
