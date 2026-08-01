# ADR 0010: Allocation Candidates Are Not Capital Reservations

## Status

Accepted for Phase 3 Milestone 1.

## Decision

An `AllocationCandidate` is an immutable calculation contract binding a
proposal to one portfolio snapshot and allocation policy. It may contain
bounded capital and canonical-master quantity limits, exposure projections,
rounding evidence, and constraints, but it cannot mutate a snapshot or reserve
capital.

## Consequences

Milestone 1 repositories may persist candidates and decisions independently
for contract testing but must not simulate transactionality. The future atomic
boundary will publish decision, reservation, and revision `N+1` together.
