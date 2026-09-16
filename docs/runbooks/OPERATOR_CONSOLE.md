# Read-only operator console

## Scope and launch

The application embeds the frontend at `/console/` on its existing HTTP address.
No Node, package install, CDN, credentials, chart service or frontend build is
required. Assets ship in the Go binary. Rebuild/restart after asset edits.

The console is a SHADOW operator view, not a full PAPER account terminal. It
provides overview, per-underlying strategy warmup, session scorecards, service
responses and daily preparation guidance. It exposes no order, configuration,
kill-switch, authorization, credential-entry or broker-mutation controls.

For an entirely offline preview, from the repository root:

```powershell
go run ./cmd/tradeedge-console-demo
```

Visit `http://127.0.0.1:8090/console/`. Use `-address 127.0.0.1:8091` if the
default port is occupied. The executable accepts only literal loopback addresses,
does not load application configuration, and constructs no external clients.
Stop it with Ctrl+C. No mock endpoint is mounted in the production composition.

## Daily preparation

Follow [the M4 runbook](PHASE_8_M4_LIVE_SHADOW.md) to produce the current reviewed
calendar, bounded mappings, runtime bundle, external evidence and authorization.
The console checklist does not execute these actions or mark them complete.

Inspect the existing packet without connecting to any provider:

```powershell
go run ./cmd/tradeedge-validation prepare-session -date YYYY-MM-DD -commit FULL_APPLICATION_COMMIT -authorization PATH_TO_EXISTING_MANIFEST
```

Use the intended deployed binary's full hexadecimal commit, not a branch name.
The command verifies the existing authorization and linked artifacts using the
released authorization loader. It checks SHADOW qualification-only scope,
prohibited mutations, commit binding, requested/today's IST date, and the current
validity window. It does not verify the binary itself, probe current connectivity,
inspect a running process or guarantee readiness at a later startup time.

Omit `-authorization` to see a blocked checklist before a packet exists. JSON is
written to stdout; diagnostics go to stderr. Exit 1 means blocked or invalid
arguments. Exit 0 reports `ARTIFACTS_VERIFIED_OPERATOR_APPROVAL_REQUIRED`, never
permission to start or trade. No artifact is written or authority issued.
Explicit operator approval and all existing runtime checks are still required.

## Reading the frontend

- Process health is liveness only, not permission to trade.
- Warmup is completed candles, independently for NIFTY and BANKNIFTY. Fifty
  samples alone do not qualify a strategy or prove mappings/risk are ready.
- The SHADOW API reports broker orders disabled; an unavailable API displays
  unknown, not disabled or ready. PAPER-only deployments lack SHADOW endpoints
  and show their absence instead of simulated data.
- Service rows report HTTP response availability. Inspect each response for its
  domain state; HTTP 200 alone does not establish integration health.
- Polling is serialized, every five seconds after the preceding refresh, with
  a five-second request timeout. Failures replace prior values with unavailable
  state. A refresh-in-progress banner identifies the previous snapshot.
- Rendered API text uses text nodes, not HTML. Assets enforce a self-only CSP,
  no framing, no-store caching and content-type protection.
- Export prepares a frozen JSON diagnostic with `real_market_evidence:false`
  and an explicit mock flag. Download is browser-dependent; the read-only JSON
  text area supports selection/copy when embedded browsers suppress downloads.
  Neither download nor copy publishes or finalizes a session record.

## Mock verification

The selector is available only on the standalone mock server:

| Scenario | Expected result |
| --- | --- |
| Ready for observation | Two underlyings at 50/50; SHADOW; orders disabled |
| Strategy warming | Two underlyings at 6/50; strategy WARMING |
| Feed and notification outage | Readiness, integration and notification HTTP 503; stale data and blocked strategy |
| No session data | No underlying/scorecard rows; readiness not ready |
| Stop the mock process | Refresh clears prior healthy state; mode/orders unknown |

API fixtures test presentation only. The accelerated 375-minute domain replay
test separately verifies warmup/crossovers cannot bypass missing derivative
mappings: zero risk calls, zero accepted signals and invalid session scorecards.
Existing released pipeline tests cover risk, execution, reconciliation, restart,
qualification and the once-only shutdown fix. None of these tests produces
real-market evidence or replaces a full authorized market observation session.

## Verification performed (2026-09-16)

- `go test ./...`, `go vet ./...`, `go build ./...` passed locally.
- New HTTP, mock scenario, command-input, date/expiry/commit/scope and missing
  authorization tests passed, including the accelerated mapping-failure replay.
- Browser: healthy/warming/degraded/empty scenarios, session table, preparation
  navigation, diagnostic preview and disconnected-server clearing checked.
- Desktop and 390px mobile view inspected; mobile document had no horizontal
  overflow. JSON download event could not be confirmed in the embedded browser;
  the visible JSON copy fallback was verified.
- Local race execution is unavailable because this Windows Go environment has
  CGO disabled. The existing Linux CI workflow runs `go test -race ./...`; it has
  not been run for these uncommitted changes.

## Security and remaining work

The existing operational HTTP server does not add authentication here. Keep it
on a trusted loopback/private network or behind an authenticated operator gateway;
do not expose the console/API publicly. Diagnostics may contain operational
identifiers: review before sharing, and never put credentials into API payloads.

This milestone does not implement durable PostgreSQL accounting, a PAPER
positions/P&L terminal, strategy alpha qualification, real session evidence,
deployment approval or LIVE trading. Full-day real-data verification still
requires a fresh operator-approved session. The staged August 13 evidence is
unchanged and is not repurposed as test output.
