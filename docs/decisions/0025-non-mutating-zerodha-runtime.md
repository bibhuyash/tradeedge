# ADR 0025: Non-Mutating Zerodha Runtime

## Status

Accepted for Phase 5 Milestone 3.

## Decision

Zerodha integration mode is explicit and restart-bound: `OFFLINE`, `PAPER`,
`SHADOW`, or `LIVE_DISABLED`. The zero value is `OFFLINE`; unknown values are
configuration errors. `LIVE_DISABLED` is an observable blocked state, not a
trading mode. The global runtime continues to accept paper trading only.

PAPER execution uses the provider-neutral Phase 4 OMS with a deterministic
paper broker. Canonical Zerodha quote observations may generate simulated fills
only when executable best depth exists after submission. SHADOW adds the exact
M2 request translation and a bounded fingerprinted decision record before
delegating to the same paper broker. Neither mode owns an order mutation
transport.

One adapter-local stream supervisor owns connection, subscriptions, bounded
reconnects, order-update decoding, and shutdown. Disconnect is degraded
connectivity, never broker rejection. Phase 4 reconciliation remains
authoritative for paper OMS; Zerodha GET evidence never fabricates paper state.

## Consequences

Phase 5 can close with operational paper/shadow evidence while real order
mutation remains unreachable from runtime composition. Durable database
storage, positions/P&L, and any live capability remain later work requiring a
new decision and approval.
