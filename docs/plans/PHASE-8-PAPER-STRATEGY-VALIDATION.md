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

### M1 selected candidate

The single candidate is `nifty-ema-crossover-paper` version 1:

- signal: canonical completed one-minute NIFTY 50 candles;
- execution: one explicitly configured tradable PAPER instrument, never the
  observation-only index and never an instrument selected dynamically;
- entry: the fixed-point EMA20 value crosses from less-than-or-equal to greater
  than EMA50;
- exit: the fixed-point EMA20 value crosses from greater-than-or-equal to less
  than EMA50, plus the existing runtime EOD drain responsibility;
- sizing: one configured lot, one simultaneous position intent, 10% strategy
  budget intent; Phase 3 retains final authority;
- session: `NORMAL_TRADING` only; the candidate is `CAS_RESTRICTED`;
- arithmetic: integer minor units scaled by 1,000,000, alpha `2/(period+1)`,
  rounding half away from zero after every recurrence;
- seed: the first canonical close in the bounded 64-candle frame; at least 50
  samples are required before a relation can be established. Recomputing the
  bounded window avoids hidden uncheckpointed indicator history.

The checked-in configuration is disabled. The repository's current real
instrument master has no approved tradable execution mapping for this
candidate. `FULL_PIPELINE` activation therefore remains fail-closed until a
session-specific, checksum-authorized mapping and execution price series are
provided. M1 does not reinterpret NIFTY 50 as tradable and does not add option
selection.

### M1 implementation checklist

- [x] One versioned production-candidate definition; engineering fixture unchanged.
- [x] Fixed-point EMA policy and known-sequence tests.
- [x] Edge-triggered bullish entry and bearish exit proposal tests.
- [x] Canonical completed-candle frame with separate execution-price series.
- [x] Strict configuration, one lot, one position intent, disabled default.
- [x] Explicit bounded NO_ACTION reason vocabulary.
- [x] Bounded GET-only validation status contract.
- [x] Corrupt configuration, cancellation, and overflow fail closed.
- [ ] Enable a real-session execution mapping (requires separately reviewed evidence).
- [ ] Run connected strategy-to-accounting PAPER sessions (M2 activation gate).

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

## M2 derivatives amendment

NIFTYBEES is excluded. M2 establishes NIFTY spot for EMA signal authority, a
checksum-resolved NIFTY future for forward strike context, and one selected
NIFTY call's own quote for execution and valuation. SHADOW has no fills and
PAPER is limited to one long option lot. The candidate remains disabled.

## M3 shadow qualification amendment

M3 generalizes the checksum-mapped derivatives boundary to exactly NIFTY and
BANKNIFTY and records their evidence independently. `EMA_REFERENCE_V1` remains
a disabled reference candidate. The qualification engine records conservative
option-side entry/exit references, +1/+5/+15/+30 minute horizons, MFE/MAE,
typed data-quality failures, transparent decision-time regime tags, and integer
scorecards in durable SHADOW checkpoints. It cannot create OMS orders, fills,
authoritative positions, or broker calls. Sample eligibility never promotes the
candidate; `QUALIFIED` requires an explicit reviewed transition and still grants
no PAPER or LIVE authority.
