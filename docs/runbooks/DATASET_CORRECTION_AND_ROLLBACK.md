# Runbook: Dataset Correction and Rollback

## Correct

1. Run `verify` on the parent.
2. Run `rebuild` with corrected input, master, calendar, parent, series, reason, and stable request ID.
3. Verify and replay the child.
4. Run `publish` with the observed current dataset as `-expected-current`.

## Roll Back

Run `rollback` with the verified earlier dataset and current dataset as `-expected-current`. Rollback appends a generation; it never deletes history.

## Safety

Retry with the same request ID. A conflict means state changed or content disagrees; investigate rather than forcing publication.
