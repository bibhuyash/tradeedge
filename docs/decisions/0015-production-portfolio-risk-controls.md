# ADR 0015: Pure Production-Style Portfolio-Risk Controls

## Status

Accepted for Phase 3 Milestone 3.

## Decision

Phase 3 registers ten statically versioned, pure rules covering capital, strategy allocation, daily loss, drawdown, instrument and underlying exposure, portfolio-wide open exposure, reserve capital, kill switch, and circuit breaker. Rules use checked integer arithmetic and immutable runner inputs. Unknown, unavailable, inconsistent, overflowing, or missing risk-bearing values defer and fail closed.

Safe capital reductions are explicit `MODIFICATION_REQUIRED` results with lot-aligned bounds. Loss and blocking control states are not repairable by resizing and therefore reject. Rules perform no I/O or state mutation.

## Consequences

Rule evidence remains deterministic and auditable. Instrument, underlying, and portfolio-wide projections are produced provider-neutrally by allocation. Control changes, when later supplied, must remain inside the existing atomic publication boundary.
