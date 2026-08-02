# ADR 0020: Provider-Neutral Execution Reconciliation

## Status

Accepted for Phase 4 Milestone 2.

## Decision

Reconciliation consumes a complete provider-neutral broker snapshot keyed by
stable client-order identity and compares it with bounded OMS reads. Broker
facts may repair submitted, acknowledged, filled, rejected, cancelled, and
causally valid late-fill state only by publishing a deterministic broker event
through the existing atomic OMS boundary.

Missing broker orders, unknown broker orders, term mismatches, broker fill
regression, incomplete snapshots, and unpublishable state mismatches remain
explicit critical issues and block safe continuation. Reconciliation never
guesses that silence means rejection or that absent state means zero.

The paper event cursor, paper broker checkpoint, and M1 order checkpoints are
restorable. Serial replay from the same logical steps must produce identical
TradeEdge identities, canonical state, reports, and fills. Startup ownership
and durable scheduling remain composition concerns for later milestones.

## Consequences

Some uncertainty requires operational intervention. Broker truth corrects the
local understanding of an order but does not bypass transition, overfill,
identity, or atomic-publication validation.

## Rejected Alternatives

Direct reconciliation writes bypass audit and atomicity. Treating an incomplete
snapshot as authoritative can erase real exposure. Last-write-wins hides term
and fill conflicts.
