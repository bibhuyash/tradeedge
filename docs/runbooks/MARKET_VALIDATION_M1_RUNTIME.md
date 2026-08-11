# Market Validation M1 Runtime

This is a PAPER-only observation composition, not Phase 8. `cmd/tradeedge`
loads a checksum-pinned runtime bundle, restores a verified checkpoint, validates
the Zerodha read-only session, connects the quote WebSocket, and admits canonical
Phase 1 events into the Phase 7 runtime. The only broker in the execution graph
is the deterministic observed PAPER broker.

## Runtime bundle

`TRADEEDGE_RUNTIME_BUNDLE` points to strict JSON with schema
`tradeedge-runtime-bundle/v1`, mode `PAPER`, and six `{path, sha256}` references:
`calendar`, `instrument_master`, `watchlist`, `strategies`, `portfolio`, and
`risk`. Paths are resolved relative to the bundle. The M1 strategies file must
use schema `market-validation-strategies/v1` and contain zero instances. Any
unknown field, checksum mismatch, missing mapping, non-quote requirement, stale
session, or sensitive-looking path fails startup closed.

The watchlist contains one to four required Zerodha quote mappings. WebSocket
packets are converted to integer-minor-unit observations, then normalized and
ordered by Phase 1. Reconnects always resubscribe the complete pinned token set.
Text order updates are rejected as a read-only boundary violation.

## Restoration and controls

`TRADEEDGE_CHECKPOINT_ROOT` contains immutable, versioned generations and an
atomically published `CURRENT` pointer. Component and aggregate SHA-256 values
are verified before Phase 7 can activate. Configuration/calendar changes and
corruption fail closed. Credentials are rejected from checkpoint material.

`TRADEEDGE_OPERATOR_CONTROL_SOCKET` is a mode `0600` Unix socket. Commands are
checksummed, atomic, auditable, idempotent by request ID, and one-way:
`STOP_NEW_EXPOSURE` latches new exposure off; `EOD_CLOSE` latches it off, drains
Phase 7, checkpoints, and shuts down. There are no buy, sell, strategy-enable,
risk-threshold, broker cancellation, or Telegram control endpoints.

## Readiness

`/readyz` reports both live market readiness and the actual Phase 7 snapshot.
The runtime remains NOT_READY until restore, session authentication, stream
subscription, fresh required quotes, mappings, calendar, controls, PAPER broker,
OMS, accounting, valuation, reconciliation, and configuration are healthy.
With zero configured strategies observations and operations continue while
proposal, order, and fill counts remain zero.

The canonical Windows-hosted operator procedure is
`docs/runbooks/DAY0_PAPER_OPERATIONS.md`. It uses Linux Docker Compose for the
Unix-socket control boundary, documents the one-time request-token lifecycle,
and exposes accepted latest quotes at
`GET /api/v1/market-data/observations/latest`.
