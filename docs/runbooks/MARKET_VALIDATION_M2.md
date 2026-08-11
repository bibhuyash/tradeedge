# Market Validation Enablement M2

## Authority boundary

M2 prepares immutable configuration and evidence tooling. It starts neither
Day 0 nor Day 1. Only `OFFLINE`, `PAPER`, `SHADOW`, and `LIVE_DISABLED` are
permitted. The PAPER capital of INR 10,00,000 is hypothetical and grants no
authority over real capital. Zerodha is read-only; Telegram is outbound-only
and cannot authorize or control trading.

The approved strategy is `NONE`. Stop after configuration enablement and report
`STRATEGY_BLOCKED` for Day 1. Do not promote a fixture or construct a strategy
inside M2.

## Current configuration preparation

1. Produce a reviewed NSE calendar with explicit entries for every date in its
   declared coverage. Record HTTPS circular references and their SHA-256 values
   in a `market-validation-calendar-sources/v1` file together with each locally
   reviewed source path. Run `calendar-check` with
   exact `-from` and `-to` dates. When CAS is required, every trading day must
   contain ordered PRE_CAS, CAS, and POST_CAS regimes.
2. Obtain the current Zerodha instrument CSV through the approved credentialed
   operator workflow; do not commit it. Run `generate-mappings` with
   `configs/validation/mapping-selection.day0.json` and same-session RFC3339
   validity bounds (maximum 24 hours). The generator rejects ambiguous or
   duplicate mappings, expired derivatives, wrong segment/type, bad lot/tick
   metadata, missing canonical identity, and stale validity. The source digest
   is embedded in the generated master.
3. Construct the existing versioned runtime bundle over the generated calendar,
   master, watchlist and `strategies-disabled.json`, plus the checked-in PAPER
   portfolio/risk files. All artifact hashes are pinned.
4. Prepare an `OPERATIONS_ONLY` authorization draft for the exact commit,
   trading date, evidence root, bundle and artifact identities. The strategy
   must be `NONE`, `strategies-disabled/v1`, `NONE`, `CAS_DISABLED`, disabled.
   Run `authorize`; it recalculates every artifact identity and creates the
   checksummed manifest once. No secret or sensitive Telegram identifier is
   allowed in it.

The application requires `TRADEEDGE_AUTHORIZATION_MANIFEST` in PAPER mode and
fails before broker authentication when the manifest, commit, date, mode,
bundle checksum, portfolio capital, risk policy, or strategy identity differs.

## Pre-session Zerodha authentication

Use `tradeedge-zerodha-auth` before runtime startup. The utility composes only
the existing read-only session, profile client, and market stream. It does not
start `cmd/tradeedge`, Phase 7, a strategy, a broker mutation adapter, or Day 0.

Register the operator-controlled redirect URL in the Zerodha developer console
for the API key before using the utility. Zerodha selects that registered URL;
it is not a login-URL parameter and TradeEdge does not host a redirect handler.
Securely inject `TRADEEDGE_ZERODHA_API_KEY` and set the read-only boundary:

```powershell
$env:TRADEEDGE_ZERODHA_READ_ONLY='true'
go run ./cmd/tradeedge-zerodha-auth login-url
```

Open the printed URL manually. After successful login, copy the one-time
`request_token` from the registered redirect URL into the approved secret
injection mechanism as `TRADEEDGE_ZERODHA_REQUEST_TOKEN`. Also inject
`TRADEEDGE_ZERODHA_API_SECRET`; never place either value in a command argument,
tracked file, evidence, log, or ticket. The standalone bounded exchange
diagnostic is:

```powershell
go run ./cmd/tradeedge-zerodha-auth exchange-token -timeout 10s
```

It prints only `AUTHENTICATED` and the calculated expiry. The access token is
held in memory, cleared on shutdown, and never printed or persisted. Running
`exchange-token` and then running `verify-rest` or `verify-websocket` cannot
hand that session to the new process. Do not exchange the same one-time
`request_token` again.

For the complete Day-0 authentication and connectivity check, obtain one fresh
`request_token` and run the single-process preflight. It validates the pinned
bundle before consuming the token, exchanges exactly once, and retains the
same in-memory session for the read-only profile and market-stream checks:

```powershell
go run ./cmd/tradeedge-zerodha-auth preflight -runtime-bundle '.cache\market-validation\config\runtime-bundle.json' -timeout 15s -max-age 5s
```

Require `AUTHENTICATION=PASS`, `REST_AUTH=PASS`, `WEBSOCKET_AUTH=PASS`,
`OBSERVATIONS_RECEIVED=2`, and `SHUTDOWN=PASS`. The expiry is safe to record,
but the access token is never output or persisted. The separate verification
commands below remain diagnostics for an access token and expiry injected
through the approved secret mechanism; they do not reuse an earlier CLI
process's session.

On WebSocket failure, preflight also prints bounded, credential-free stage
counters for handshake, subscription, binary frames, heartbeats, packet decode,
index packets, token matches, and fresh observations. `LAST_FAILURE_STAGE` is a
safe enum and never includes the authenticated endpoint. Zerodha may deliver an
initial timestamp-less 28-byte index quote before the requested full mode takes
effect. The parser accepts the documented 8-, 28-, 32-, 44-, and 184-byte
formats, but only timestamped packets can satisfy the unchanged freshness gate.

Verify only the fixed read-only profile endpoint:

```powershell
go run ./cmd/tradeedge-zerodha-auth verify-rest -timeout 10s
```

Require `REST_AUTH=PASS`. To diagnose market connectivity with an independently
injected session, verify the checksum-pinned Day-0 NIFTY 50 and NIFTY BANK
quote mappings. The utility rejects every other watchlist, waits for a fresh
observation from both provider tokens, and disconnects cleanly within the
bound:

```powershell
go run ./cmd/tradeedge-zerodha-auth verify-websocket -runtime-bundle '.cache\market-validation\config\runtime-bundle.json' -timeout 15s -max-age 5s
```

Require `WEBSOCKET_AUTH=PASS`, `OBSERVATIONS_RECEIVED=2`, and `SHUTDOWN=PASS`.
Any missing credential, exchange failure, expired session, changed bundle,
unexpected token, stale observation, timeout, or disconnect fails closed. None
of these checks authorizes or starts Day 0.

## Telegram acceptance

With credentials injected only into environment variables, generate create-once
evidence for `-kind test`, `-kind critical`, and, after drain, `-kind eod`.
Artifacts contain delivery status and a deterministic notification identity,
but neither token nor chat identifier. Provider health plus all three delivery
classes are required before marking `telegram_verified` in Day-0 evidence.

## Day 0 gate

Day 0 may begin only after explicit operator/CEO approval. Use PAPER,
read-only real Zerodha observations, the two-index watchlist, and zero active
strategies. Capture session, market data, readiness, WebSocket, mapping, CAS,
Telegram, checkpoint, EOD drain, and shutdown evidence. Populate a copy of
`day0-evidence.example.json`, then run `day0-gate`.

PASS requires every evidence flag, at least 99.50% readiness, no gap over 60
seconds, and exactly zero active strategies, proposals, orders, fills, and real
broker mutations. Any mismatch is fail-closed and the evidence remains
non-authoritative. Do not infer failed broker activity from a timeout.

## Day 1 gate

`day1-gate` requires a finalized Day-0 PASS and a separate `FULL_PIPELINE`
authorization manifest that pins the Day-0 gate artifact and names one enabled
`PRODUCTION_CANDIDATE`. Because the
approved M2 strategy is `NONE`, such a manifest is rejected with
`STRATEGY_BLOCKED`; Day 1 is not ready. PAPER results retain the classification
`OPERATIONAL_PAPER_PNL` because the current broker model is not evidence of
live spread, liquidity, latency, or slippage.

## Commands (do not start a session)

```text
tradeedge-validation calendar-check -calendar ... -sources ... -from YYYY-MM-DD -to YYYY-MM-DD -output ...
tradeedge-validation generate-mappings -dump ... -selection configs/validation/mapping-selection.day0.json -as-of ... -valid-from ... -valid-until ... -master-output ... -watchlist-output ...
tradeedge-validation authorize -input ... -output ...
tradeedge-validation readiness -config ... -output ... -repo .
tradeedge-validation day0-gate -evidence ... -authorization ... -output ...
tradeedge-validation day1-gate -day0 ... -authorization ... -output ...
```

All outputs are create-once. Review the generated M2 CI artifact before CEO
approval. CI enablement PASS means tooling passed; its explicit
`day0_ready:false` and `day1_ready:false` values mean no observation or strategy
session has been authorized or executed.
