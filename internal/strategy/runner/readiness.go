package runner

import (
	"context"

	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

// ReadinessSnapshotSource is implemented by the Phase 1.1 readiness evaluator.
// Historical replay may provide an equivalent dataset-scoped snapshot source.
type ReadinessSnapshotSource interface {
	Snapshot(context.Context) readiness.Snapshot
}

// SnapshotReadinessGate adapts the existing market-data readiness model without
// independently inferring market state in the strategy runner.
type SnapshotReadinessGate struct {
	source ReadinessSnapshotSource
}

func NewSnapshotReadinessGate(source ReadinessSnapshotSource) (*SnapshotReadinessGate, error) {
	if source == nil {
		return nil, ErrReadinessBlocked
	}
	return &SnapshotReadinessGate{source: source}, nil
}

func (gate *SnapshotReadinessGate) Evidence(
	ctx context.Context,
	_ strategymodel.StrategyInstance,
	frame strategymodel.CandleFrame,
) strategymodel.ReadinessEvidence {
	snapshot := gate.source.Snapshot(ctx)
	evidence := strategymodel.ReadinessEvidence{
		State: snapshot.State, Reasons: append([]readiness.ReasonCode(nil), snapshot.Reasons...),
		PolicyVersion: snapshot.PolicyVersion, CalendarVersion: snapshot.CalendarVersion,
		EvaluatedAt: snapshot.EvaluatedAt,
	}
	if ctx.Err() != nil {
		return blockedEvidence(evidence, readiness.ReasonProviderUnavailable)
	}
	if snapshot.State != readiness.StateReady || !snapshot.TradingPermitted {
		return evidence
	}
	if frame.CalendarVersion() == "" || frame.CalendarVersion() != snapshot.CalendarVersion {
		return blockedEvidence(evidence, readiness.ReasonCalendarOutOfRange)
	}
	for _, subscription := range frame.Subscription().Subscriptions() {
		if !subscription.Required {
			continue
		}
		found := false
		for _, diagnostic := range snapshot.Diagnostics {
			if diagnostic.InstrumentID == subscription.InstrumentID && diagnostic.Required &&
				diagnostic.EventKind == marketmodel.EventKindCandle &&
				diagnostic.Interval == subscription.Interval &&
				diagnostic.State == readiness.StateReady {
				found = true
				break
			}
		}
		if !found {
			return blockedEvidence(evidence, readiness.ReasonCoverageIncomplete)
		}
	}
	if evidence.EvaluatedAt.IsZero() {
		return blockedEvidence(evidence, readiness.ReasonPolicyInvalid)
	}
	evidence.Reasons = []readiness.ReasonCode{readiness.ReasonNone}
	return evidence
}

func blockedEvidence(
	evidence strategymodel.ReadinessEvidence,
	reason readiness.ReasonCode,
) strategymodel.ReadinessEvidence {
	evidence.State = readiness.StateUnknown
	evidence.Reasons = []readiness.ReasonCode{reason}
	return evidence
}
