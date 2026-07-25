# ADR 0008: Integer and Rational Authoritative Risk Arithmetic

## Status

Accepted for Phase 3 Milestone 1.

## Decision

Capital, prices, P&L, loss, and exposure use integer minor units. Percentages
use integer basis points. Leverage uses a reduced integer rational. Signed
money is allowed where the domain is signed; capital buckets remain
non-negative. Checked addition, subtraction, and multiplication reject
overflow.

Binary floating point is prohibited in authoritative Phase 3 structures.

## Consequences

Calculations are replay-stable and explicit about rounding. Future Greek values
must use reviewed fixed scales rather than floating point; Milestone 1 does not
pretend unavailable Greeks are known.
