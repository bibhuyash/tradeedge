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

## Deferred to Milestone 2

Order translation, submission, cancellation, broker order IDs, order updates,
UNKNOWN recovery, and broker-state reconciliation are intentionally absent.

## Operational Safety

The main runtime remains `paper` by default and rejects live trading mode. M1
is not automatically composed into application startup, so credentials and
network access are unnecessary for normal operation. A future controlled
read-only composition must explicitly load the adapter configuration and
credentials and must keep `order_mutation_permitted=false`.

M1 recognizes `TRADEEDGE_ZERODHA_READ_ONLY`, `TRADEEDGE_ZERODHA_BASE_URL`,
`TRADEEDGE_ZERODHA_TIMEOUT`, `TRADEEDGE_ZERODHA_MAX_CONCURRENCY`,
`TRADEEDGE_ZERODHA_READ_RETRIES`, and `TRADEEDGE_ZERODHA_MAPPING_MAX_AGE`.
The runtime credential source reads the API key, API secret, optional one-time
request token, optional access token, and mandatory explicit access-token
expiry from their `TRADEEDGE_ZERODHA_*` environment names. Credential variables
are deliberately absent from `.env.example`; production must supply them from
an approved runtime secret mechanism.
