# Operator console and daily preparation

Bounded implementation plan:

1. Add an offline `prepare-session` inspection command that verifies an existing
   SHADOW authorization and its linked artifacts, date, commit and validity window.
   Emit diagnostic JSON and a nonzero exit when blocked; never issue authority.
2. Embed a dependency-free, read-only operator console at `/console/` in the
   existing HTTP server. Show readiness, per-underlying warmup, session results,
   service responses and the daily preparation sequence.
3. Provide a separate loopback-only mock executable with deterministic healthy,
   warming, degraded and empty scenarios. It must not load credentials, connect
   to brokers, or create real-market evidence.
4. Test authorization failure boundaries, HTTP contracts, frontend rendering and
   mock scenarios; run Go tests, vet and build. Preserve staged operator evidence.

Safety: display unavailable data as unknown, never infer trading permission from
process health, never substitute mock data for failed real APIs, and expose no
mutation controls. Mock API fixtures validate presentation, not market readiness.
Existing deterministic domain tests remain the trading-pipeline verification.
