# ADR 0013: Atomic Portfolio Risk Publication

## Status

Accepted for Phase 3 Milestone 2.

## Decision

One provider-neutral transaction compares portfolio revision and checkpoint
checksum, validates the full parent/child lineage, then commits the allocation
candidate, risk evaluation, decision, optional capital reservation, and next
portfolio checkpoint/snapshot together. The deterministic decision trigger is
the deduplication identity. Exact content returns the committed receipt;
identity reuse with different content is rejected. A stale revision is a typed
conflict and is never retried against newer state.

APPROVED and MODIFIED decisions move approved capital from available to
reserved in revision `N+1`. REJECTED and DEFERRED decisions publish their audit
decision and an economically unchanged `N+1` snapshot without a reservation.
The reference implementation is bounded in-memory storage; a durable database
transaction is deferred.

## Consequences

Any validation, cancellation, timeout, panic, conflict, or injected storage
failure before commit leaves every authoritative view unchanged. Checkpoint
lineage supports verified restoration and deterministic continuation. A
reservation is portfolio state, not an order or executable authorization.

## Rejected Alternatives

Separate decision and snapshot writes permit partial authority. Silent retry on
the newest revision changes the decision inputs. Eventual compensation cannot
provide the required all-or-nothing invariant.
