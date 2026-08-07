# Phase 5: Zerodha Broker Adapter and Controlled Integration

## Scope

Phase 5 adapts Zerodha to TradeEdge behind provider-neutral boundaries. Passing
any Phase 5 milestone does not authorize unrestricted live trading.

## Milestone 1: Connectivity Foundation

- [x] Isolated Zerodha adapter package with no provider-type leakage.
- [x] Disabled-by-default read-only configuration with bounded timeouts,
  concurrency, and safe GET retries.
- [x] Opaque runtime credential source with formatting and logging redaction.
- [x] Explicit login-required, token-exchange, authenticated, expired, failed,
  and stopped session states; no invented refresh.
- [x] Fixed profile and instrument read boundaries plus deterministic fake
  transport; no generic endpoint API.
- [x] Bidirectional canonical/provider mapping with immutable master version,
  validity windows, ambiguity rejection, staleness checks, and derivative
  expiry checks.
- [x] Read-only connectivity readiness and bounded capability summaries.
- [x] Provider-neutral bounded telemetry with no provider, account, instrument,
  symbol, token, path, error, or credential labels.
- [x] Deterministic timeout, throttle, malformed-response, cancellation,
  concurrent-read, restoration, shutdown, and mapping tests.
- [x] CI scans for secrets, logging, domain leakage, floating-point authority,
  and order mutation capability.
- [x] Existing Phase 4 `BrokerPort` remains unchanged and unimplemented by the
  Zerodha M1 adapter.

## Milestone 2: Guarded Order Adapter

- [x] Existing provider-neutral `BrokerPort` implemented without changing its
  interface or allowing provider types into domain packages.
- [x] Fixed-profile canonical order translation and provider-neutral execution
  reports preserve TradeEdge client/order identity and approved authority.
- [x] Mutation requires an explicit injected permit; the default gate denies
  submission and cancellation and normal startup does not compose the adapter.
- [x] Durable-checkpoint contract records exact request correlation, compact
  provider tag, mapping version, broker order ID, and uncertain delivery state.
- [x] Proven-not-sent failures alone are retryable; possibly-sent submissions
  become `UNKNOWN` and require exact broker evidence before resolution.
- [x] Bounded update journal contains duplicate, stale, out-of-order, late-fill,
  overfill, collision, and stream-gap handling.
- [x] REST snapshot translation cross-checks orders and trades and marks
  incomplete or inconsistent evidence rather than fabricating state.
- [x] Session expiry, rate limits, cancellation ambiguity, disconnects,
  restoration, concurrency, and shutdown have deterministic fake tests.
- [x] Provider-neutral bounded telemetry and CI scans cover secrets, logging,
  dependencies, float authority, live mode, and reachable order capability.
- [x] Phase 4 coordinator, OMS atomic publication, UNKNOWN recovery, and
  reconciliation semantics remain unchanged.

## Milestone 3: Non-Mutating Runtime and Release

- [x] Explicit OFFLINE, PAPER, SHADOW, and blocked LIVE_DISABLED modes.
- [x] PAPER/SHADOW require a second explicit read-only opt-in; omission fails
  closed before provider construction.
- [x] PAPER fills derive deterministically from canonical executable quote
  depth without any real broker mutation.
- [x] SHADOW captures exact translated request fingerprints and delegates only
  to the paper broker.
- [x] Adapter-owned bounded stream supervision, reconnect, resubscription,
  session-expiry, and shutdown contracts.
- [x] Aggregate session, mapping, stream, reconciliation, UNKNOWN, and adapter
  health with bounded GET-only diagnostics.
- [x] Versioned observed-paper and execution correlation checkpoints with
  deterministic restoration tests.
- [x] Bounded telemetry vocabulary with no identity or credential labels.
- [x] Ubuntu race/stress workflow, capability scans, machine-readable evidence,
  checksum, enforcement, ADR, and release runbook.

Phase 5 closure remains non-mutating. Positions/P&L, database migration,
multi-broker routing, and all live capability are outside Phase 5.

## Operational Safety

The main runtime remains `paper` by default and rejects live trading mode. M1
is not automatically composed into application startup, so credentials,
network access, and mutation authority are unnecessary for normal operation.
The M2 constructor defaults to a deny gate. Supplying the permit implementation
is necessary but not sufficient for a future deployment: runtime composition
and operational authorization remain M3 work. No unrestricted live mode exists.

M1 recognizes `TRADEEDGE_ZERODHA_READ_ONLY`, `TRADEEDGE_ZERODHA_BASE_URL`,
`TRADEEDGE_ZERODHA_TIMEOUT`, `TRADEEDGE_ZERODHA_MAX_CONCURRENCY`,
`TRADEEDGE_ZERODHA_READ_RETRIES`, and `TRADEEDGE_ZERODHA_MAPPING_MAX_AGE`.
The runtime credential source reads the API key, API secret, optional one-time
request token, optional access token, and mandatory explicit access-token
expiry from their `TRADEEDGE_ZERODHA_*` environment names. Credential variables
are deliberately absent from `.env.example`; production must supply them from
an approved runtime secret mechanism.
