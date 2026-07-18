# Runbook: Stale or Missing Market Data

## Trigger

`/readyz` is 503 with `STALE`, `NO_DATA`, or `INCOMPLETE`.

## Response

1. Confirm `trading_permitted=false`; do not bypass readiness.
2. Inspect `/api/v1/market-data/readiness` and the paginated instrument diagnostics.
3. Separate exchange-age, ingestion-age, transport-lag, clock-skew, provider, and missing-candle reasons.
4. Verify the configured calendar version and current session.
5. Preserve logs and quality records. Restore the provider only through its future bounded adapter.
6. Require a fresh `READY` evaluation; never manually relabel stale data.

## Escalation

Escalate persistent provider or clock failures. Threshold changes require CEO approval.
