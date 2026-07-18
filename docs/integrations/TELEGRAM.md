# Telegram Integration

## Scope

Telegram will provide alerts, trade notifications, status queries, and tightly controlled safety commands behind a notification interface. Phase 0 contains no Telegram code.

## Assumptions

Delivery may be delayed, duplicated, reordered, or unavailable. Chat identity alone is insufficient authorization.

## Responsibilities

The future adapter sends redacted notifications and authenticates approved operators for status, pause, paper-resume, and global kill-switch commands.

## Invariants

- Telegram cannot place orders, alter strategy policy, weaken risk, or enable live trading.
- Commands use allowlists, replay protection, confirmation where appropriate, and audit.
- Notification delivery does not determine trading state.

## Failure Modes

Compromised accounts, replayed updates, webhook spoofing, rate limiting, and delivery failure can mislead operators or trigger unauthorized actions.

## Trade-offs

Restricting commands reduces convenience but prevents a chat channel from becoming an uncontrolled trading API.

## Unresolved Questions

Bot hosting mode, operator enrollment, confirmation windows, and escalation routing require a security review.

## Acceptance Criteria

- Notification contracts remain provider-neutral.
- Safety commands are narrowly enumerated.
- Missing Telegram delivery cannot bypass local risk or kill-switch state.
