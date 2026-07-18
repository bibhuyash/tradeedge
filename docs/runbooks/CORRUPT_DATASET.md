# Runbook: Corrupt Dataset

## Trigger

`verify`, replay, lineage, or an operational API reports checksum/schema corruption.

## Response

1. Do not publish or replay the dataset.
2. Preserve the directory and logs as evidence; do not repair files in place.
3. Verify the parent and all source inputs.
4. Rebuild into a new immutable child with a stable request ID and documented reason.
5. If the corrupt dataset is current, use a compare-and-swap rollback to the last verified dataset.
