# Phase 8: Production-Candidate PAPER Strategy Validation

## Decision

Phase 7 and Market Validation Enablement have proved read-only real-market
observation and zero-authority operations. The next product milestone is not
live trading. It is the addition and evidence-based approval of one bounded
production-candidate strategy for the existing PAPER pipeline.

## Milestone 1: Candidate definition and deterministic evidence

- Select exactly one options strategy from an approved product requirement.
- Define typed parameters, eligibility, data dependencies, CAS policy, and
  explicit fail-closed behavior.
- Produce deterministic replay, property, and adversarial evidence across
  normal, stale, gapped, CAS, and restart scenarios.
- Bind the reviewed strategy/configuration identity into authorization tooling.
- Keep real broker mutation unreachable and PAPER capital/risk limits unchanged.

## Milestone 2: Full-pipeline PAPER sessions

- Authorize `FULL_PIPELINE` only after Milestone 1 and an independently passing
  Day-0 operations gate.
- Run the candidate through the existing strategy, portfolio, risk, execution,
  OMS, accounting, reconciliation, and checkpoint path using the paper broker.
- Accumulate the existing 10-20 session scorecard; no P&L result can waive an
  operational invalidity.

## Explicit exclusions

No LIVE mode, real order endpoint, broker mutation, risk relaxation,
microservice split, Kafka, or Kubernetes work belongs to Phase 8. Any live
pilot remains a separately designed and explicitly approved roadmap branch.
