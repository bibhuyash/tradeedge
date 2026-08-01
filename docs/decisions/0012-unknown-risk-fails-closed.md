# ADR 0012: Unknown Risk Values Fail Closed

## Status

Accepted for Phase 3 Milestone 1.

## Decision

Risk-bearing values distinguish KNOWN, UNKNOWN, UNAVAILABLE, and
NOT_APPLICABLE. Unknown or unavailable values carry no numeric zero.
Projection preserves uncertainty. Maximum loss separately distinguishes known,
unknown, and unbounded risk.

Canonical configuration rejects unknown rules, duplicate rules/keys, floats,
invalid limits, and contradictory control thresholds.

## Consequences

Missing facts cannot silently reduce exposure or pass a rule. Later rule
orchestration must map unavailable authoritative input to a fail-closed DEFER
or REJECT according to explicit policy.
