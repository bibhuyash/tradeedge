# ADR 0030: Calendar-Driven CAS Regimes and One SHADOW Book

## Status

Accepted for Phase 7 Milestone 1.

## Decision

PRE_CAS, CAS_ACTIVE, and POST_CAS are versioned exchange-calendar regimes.
Strategies declare CAS_SAFE, CAS_RESTRICTED, or CAS_DISABLED; they do not read
wall time to infer CAS. During CAS_ACTIVE, restricted strategies cannot open
new exposure. A centrally proven risk-reducing action may continue when the
strategy policy permits it.

Price provenance distinguishes LTP, CAS indicative/reference/equilibrium, and
official close. Phase 6 valuation continues to accept canonical LTP only.

SHADOW retains the Phase 5 contract: record the translated Zerodha request
fingerprint and delegate to deterministic paper execution. Simulated fills
produce one explicitly hypothetical TradeEdge position/P&L book. Real broker
positions remain non-comparable observations and never form a second book.

## Consequences

CAS transitions replay identically and cannot be scattered through strategy
code. Shadow evidence exercises translation and the full internal pipeline
without a reachable broker mutation transport.

