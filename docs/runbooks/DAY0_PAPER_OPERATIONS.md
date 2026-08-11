# Day-0 PAPER Operations

## Authority and environment

This procedure runs the checksum-authorized, zero-strategy, read-only market
observation composition. It never grants live-order authority. Docker Compose
is the canonical Windows-hosted execution path because the operator control
boundary is a Linux Unix-domain socket inside the container.

Copy `.env.example` to the git-ignored `.env`. Keep non-secret configuration in
the Compose file and set `TRADEEDGE_AUTHORIZATION_MANIFEST_HOST` to the exact
manifest authorized for the binary commit. Put credentials only in `.env` (or
an equivalent environment injector) and never in tracked files or evidence.

Authentication has two supported runtime paths:

1. A fresh `TRADEEDGE_ZERODHA_REQUEST_TOKEN` is exchanged exactly once and the
   resulting access token remains only in that runtime process.
2. An already obtained `TRADEEDGE_ZERODHA_ACCESS_TOKEN` is supplied with
   `TRADEEDGE_ZERODHA_ACCESS_TOKEN_EXPIRES_AT` in RFC3339 form.

The preflight command consumes and exchanges a request token in its own
process. It does not persist the access token. Therefore the same request token
cannot be reused by the runtime. Use a second fresh request token for startup,
or inject an access token and expiry through the approved secret mechanism.

## Canonical startup

From the repository root, require a clean tree and exact authorized commit:

```powershell
git status --short
git rev-parse HEAD
docker compose --env-file .env config --quiet
docker compose --env-file .env up -d tradeedge-day0
docker compose --env-file .env ps
docker compose --env-file .env logs -f --tail=100 tradeedge-day0
```

The pinned Go 1.23.4 Bookworm image builds `/tmp/tradeedge` with repository VCS
metadata and executes that binary. The repository is mounted read-only. Only
`.cache/market-validation/runtime-checkpoint` is writable on the host. The
socket lives in the container-local mode-0700 tmpfs; no Docker socket,
privileged mode, or host Unix socket is mounted.

## Read-only operations

Use another terminal:

```powershell
Invoke-RestMethod 'http://127.0.0.1:8080/healthz'
Invoke-RestMethod 'http://127.0.0.1:8080/readyz'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/runtime/status'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/market-data/readiness'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/market-data/readiness/instruments'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/market-data/observations/latest'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/strategy/runner'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/execution/health'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/notifications/health'
Invoke-RestMethod 'http://127.0.0.1:8080/api/v1/integrations/zerodha/status'
```

The latest-observation endpoint is bounded to the checksum-pinned watchlist and
contains only canonical quote events accepted by Phase 1 normalization,
deduplication, and ordering. It is provider-neutral operational evidence, not
an execution price or trading signal.

## One-way controls and shutdown

```powershell
docker compose --env-file .env exec -T tradeedge-day0 curl --fail --silent --show-error --unix-socket /run/tradeedge/operator.sock http://localhost/v1/status
docker compose --env-file .env exec -T tradeedge-day0 curl --fail --silent --show-error --unix-socket /run/tradeedge/operator.sock -H "Content-Type: application/json" -d '{"request_id":"DAY0-STOP-001","reason":"EOD_POLICY"}' http://localhost/v1/stop-new-exposure
docker compose --env-file .env exec -T tradeedge-day0 curl --fail --silent --show-error --unix-socket /run/tradeedge/operator.sock -H "Content-Type: application/json" -d '{"request_id":"DAY0-EOD-001","reason":"EOD_POLICY"}' http://localhost/v1/eod-close
docker compose --env-file .env stop -t 30 tradeedge-day0
```

Require `EOD_CLOSE=COMPLETED`, a clean final checkpoint, container exit code 0,
and zero strategies/proposals/orders/fills/real-broker mutations. Preserve logs
and checksums without credential values.

## Formal closure

The first session was partial, so its closure must not claim full-session
availability. Prepare a closure draft containing the exact checksum-addressed
authorization, preflight, Telegram, container log, runtime attestation, final
checkpoint, and operator-control artifacts, then run:

```powershell
go run ./cmd/tradeedge-validation close-day0 -input '.cache/market-validation/2026-08-11/day0-closure-draft.json' -output '.cache/market-validation/2026-08-11/day0-closure.json'
```

The tool derives `PARTIAL_SESSION_PASS`, `SESSION_PASS`, or `SESSION_FAIL` and
fails closed on mismatched identities, unsafe activity, an unclean checkpoint,
or incomplete EOD controls. A partial pass is not a full-day validation record
and is not counted by the 10-20 session scorecard.
