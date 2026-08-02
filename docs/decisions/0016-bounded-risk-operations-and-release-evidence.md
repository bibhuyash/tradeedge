# ADR 0016: Bounded Risk Operations and Release Evidence

## Status

Accepted for Phase 3 Milestone 3.

## Decision

Risk telemetry uses a provider-neutral recorder and Prometheus adapter with only registered rule and typed outcome dimensions. Risk operational APIs are GET-only, time-bounded, list-bounded, and return sanitized metadata. Authoritative release evidence is versioned JSON authenticated by an adjacent SHA-256 and generated only after ordinary, repeated, race, resource, and forbidden-capability gates pass.

## Consequences

Operational visibility cannot mutate risk state or introduce high-cardinality metric labels. Failed or cancelled workflows cannot close Phase 3. The evidence does not authorize broker or execution work.
