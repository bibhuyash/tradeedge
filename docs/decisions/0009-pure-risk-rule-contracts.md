# ADR 0009: Pure Versioned Risk-Rule Contracts

## Status

Accepted for Phase 3 Milestone 1.

## Decision

Rules are statically registered, versioned Go contracts. A rule receives one
immutable `RiskRuleInput`, performs no I/O or state mutation, calls no other
rule, uses no wall clock or randomness, honors cancellation in bounded work,
and returns one complete typed result.

Results distinguish PASS, VIOLATION, MODIFICATION_REQUIRED, DEFER, and ERROR.
Severity does not imply behavior; effect is explicit. Policies fail closed.

## Consequences

Rule execution is explainable and replayable. Dynamic scripting, policy
plugins, orchestration, and production rule implementations are deferred.
