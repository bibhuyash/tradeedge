# Telegram Notification Failure

Telegram is optional and outside trading correctness. A degraded notification
status does not justify changing risk, OMS, accounting, or session state.

1. Inspect `GET /api/v1/notifications/health`, `/queue`, and `/failures`.
2. Confirm trading readiness independently through existing runtime endpoints.
3. Classify `RATE_LIMITED`, `TRANSPORT`, `SERVER_ERROR`, `QUEUE_FULL`, or permanent configuration failure.
4. Verify configuration through the secret-management system; never paste the bot token or chat identifier into logs or tickets.
5. Allow bounded retry to finish. Do not restart repeatedly to force delivery.
6. Use internal CAS evidence and EOD reports as the operational record when Telegram is unavailable.

M2 exposes no Telegram command, webhook, polling, approval, pause, or kill-switch control.
