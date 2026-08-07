# Zerodha Integration

## Scope

Zerodha is the initial market-data and broker provider, isolated behind
provider-neutral interfaces. Phase 5 M2 adds a sealed order-adapter boundary,
but it remains uncomposed and mutation-disabled in the default runtime.

## Assumptions

REST and streaming APIs can disconnect, throttle, duplicate, reorder, delay, or return ambiguous results. Provider identifiers and order state remain external facts.

## Responsibilities

The connectivity adapter supports explicit authentication/session state, profile and
instrument reads, bounded timeouts/concurrency/safe-read retries, capability
discovery, mapping validation, readiness, redaction, and deterministic fakes.
The M2 execution adapter translates the existing provider-neutral `BrokerPort`
to a fixed Zerodha order profile. It maintains exact TradeEdge client-ID
correlation, broker-order evidence, a bounded update journal, REST order/trade
snapshots, and a serializable recovery checkpoint.

For market data, it must also report provider availability, preserve exact provider sequencing when available, maintain bounded socket-to-normalizer buffering, and fail readiness on overflow or stale delivery. Provider tokens remain mapping-table inputs and are prohibited from canonical IDs, operational responses, logs, and metric labels.

## Invariants

- No Zerodha SDK type crosses an adapter boundary.
- Strategies never receive the adapter.
- A submission timeout is reconciled before retry.
- Broker orders and positions are authoritative for actual external state.
- The default mutation gate denies submission and cancellation.
- Broker IDs supplement but never replace TradeEdge order identity.
- Possibly accepted submissions stay unknown until exact evidence is found.
- Fill quantity is monotonic, idempotent, and bounded by approved quantity.

## Failure Modes

Expired sessions, token leakage, stale instrument masters, stream gaps, throttling, ambiguous submissions, and inconsistent API views must produce explicit degraded states.

## Trade-offs

A provider-neutral boundary may not expose every Zerodha feature, but it protects domain behavior and testability.

## Unresolved Questions

Retail access tokens expire at the documented daily session boundary and
require re-login after expiry or invalidation. M1 does not use refresh tokens.
Production secret storage, instrument licensing/retention, runtime composition,
durable checkpoint storage, and operational login ownership remain M3 or
deployment decisions.

## Acceptance Criteria

- Default runtime operation makes no Zerodha call and requires no credential.
- M2 adapter behavior is context-aware, bounded, redacted, and deny-by-default.
- Provider-specific limitations are contained within the adapter.
- No normal runtime path can reach Zerodha submission or cancellation.
- Phase 4 owns all OMS state/report/fill publication without bypasses.

## Phase 5 Runtime Modes

`OFFLINE` is the deny-by-default zero value. `PAPER` consumes canonical Zerodha
observations but uses simulated execution. `SHADOW` additionally records the
exact would-be request fingerprint before simulated execution.
`LIVE_DISABLED` is blocked and non-mutating. No mode composes the M2 mutation
transport into normal runtime startup.
PAPER and SHADOW additionally require the explicit
`TRADEEDGE_ZERODHA_READ_ONLY=true` opt-in; omission fails configuration before
any provider component is created.

One adapter-owned stream supervisor handles quote subscriptions and text order
updates with bounded reconnects. Disconnect, overflow, session expiry, stale
mapping, UNKNOWN orders, or reconciliation disagreement blocks readiness and
never becomes rejection evidence.
