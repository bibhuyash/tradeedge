# Runbook: Missing Candles

## Trigger

Readiness is `INCOMPLETE` with `MISSING_CANDLE`, or quality output contains missing ranges.

## Response

1. Verify the exact calendar version, query bounds, interval, and session break.
2. Confirm the window is fully closed plus completion grace.
3. Do not synthesize a candle or mutate the published dataset.
4. Obtain a corrected source fixture and use the correction rebuild workflow.
5. Verify the child, publish with expected-current, then replay it before considering the incident resolved.
