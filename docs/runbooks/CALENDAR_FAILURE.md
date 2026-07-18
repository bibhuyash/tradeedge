# Runbook: Calendar Failure

## Trigger

Readiness reports `CALENDAR_UNAVAILABLE` or `CALENDAR_OUT_OF_RANGE`.

## Response

1. Stop market-data-dependent evaluation; the state is `UNKNOWN`.
2. Verify the fixture schema, checksum-derived version, timezone, explicit date coverage, and non-overlapping minute-aligned sessions.
3. A previously verified version may be reused only inside its effective range.
4. Publish a corrected calendar as a new version; never edit historical versions.
5. Re-evaluate readiness and affected historical completeness.
