# Telegram Integration

## Scope

Phase 7 M2 provides optional outbound operational alerts behind a provider-neutral
notification interface. It has no inbound updates, queries, approvals, or commands.

## Assumptions

Delivery may be delayed, duplicated, reordered, or unavailable. Chat identity alone is insufficient authorization.

## Responsibilities

The adapter sends concise redacted PAPER/SHADOW notifications. Internal events,
CAS evidence, and EOD reports remain authoritative operational evidence when
Telegram is delayed or unavailable.

## Invariants

- Telegram cannot place orders, alter strategy policy, weaken risk, or enable live trading.
- Notification delivery does not determine trading state.
- Bot tokens and destination identifiers never appear in logs, metrics, APIs, or evidence.

## Failure Modes

Rate limiting, timeout, duplicate delivery, and outages can mislead operators;
bounded internal evidence must therefore be inspected independently.

## Trade-offs

Restricting commands reduces convenience but prevents a chat channel from becoming an uncontrolled trading API.

## Configuration

Set `TRADEEDGE_TELEGRAM_ENABLED=true` with bot token and chat identifier supplied
through secure runtime configuration. When disabled, neither value is required.
Configuration errors are sanitized and never echo either secret.

## Acceptance Criteria

- Notification contracts remain provider-neutral.
- No inbound or command/control capability is reachable.
- Missing Telegram delivery cannot bypass local risk or kill-switch state.
