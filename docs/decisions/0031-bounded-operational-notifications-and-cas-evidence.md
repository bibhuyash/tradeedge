# ADR 0031: Bounded Operational Notifications and CAS Evidence

## Status

Accepted for Phase 7 Milestone 2.

## Decision

TradeEdge composes immutable operational events only from committed Phase 2–7
facts. A fixed worker pool delivers optional outbound Telegram presentation
through reserved, bounded severity queues. Admission and delivery failures are
evidenced but never propagate into trading readiness or authority. Duplicate
identity and repeated readiness states are suppressed deterministically.

CAS evidence and end-of-day reports are checksummed internal records independent
of Telegram. Missing CAS prices and incomplete financial values remain explicitly
unavailable. Default replay produces these internal records while suppressing
external delivery.

## Consequences

An extended Telegram outage ends in bounded terminal failures rather than an
unbounded retry backlog. M2 history is bounded and in-memory, so restart-durable
operational retention is deferred. No inbound Telegram or CAS strategy exists.
