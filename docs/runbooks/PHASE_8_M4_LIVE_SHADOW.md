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

`.env` is the single operator-controlled source for credentials and the two
generated artifact selectors. Do not export process-scoped TradeEdge variables
or run host Go commands. From a clean, merged `main` checkout run:

```powershell
docker compose --env-file .env run --rm tradeedge-prepare
```

If the result is `SESSION_PREPARATION=LOGIN_REQUIRED`, open the one printed
`LOGIN_URL`, complete Zerodha login, replace only
`TRADEEDGE_ZERODHA_REQUEST_TOKEN` in `.env`, and run the same command again.
The rejected one-time request token is cleared and is never retried. A valid
persisted access session is reused; otherwise the fresh request token is
exchanged once and only the access token and expiry are atomically persisted.
No token or secret is printed or placed in evidence.

`SESSION_PREPARATION=READY` means the command has completed the exact-date
calendar checks, authenticated instrument snapshot, bounded NIFTY/BANKNIFTY
mapping generation, runtime bundle, Telegram check, read-only Zerodha
preflight, authorization creation and inspection, and atomic `.env` selector
update. Existing create-once evidence is resumed after a failure; it is not
silently overwritten. A closed market date, dirty checkout, non-`main` branch,
stale process override, checksum conflict, invalid mapping, Telegram failure,
preflight failure, or authorization failure returns
`SESSION_PREPARATION=BLOCKED`.

Only after `READY`, start the canonical service:

```powershell
docker compose --env-file .env up -d tradeedge-shadow
```

Confirm `/api/v1/integrations/zerodha/status` reports read-only SHADOW and
`/api/v1/shadow/runtime` reports broker orders disabled. Candidate warmup is not
a system failure. Stop on mapping conflict, checkpoint failure, unexpected order
frame, or authorization expiry.

The packet is session-specific. Do not create an authorization for a closed or
future session merely to complete preparation. On the actual trading date run
the preparation command to generate fresh date-bound artifacts. The browser
login and request-token replacement described above are the only manual
authentication actions.

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
