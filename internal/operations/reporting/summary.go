// Package reporting creates deterministic, non-authoritative trading-day
// operational summaries from observed committed events and financial state.
package reporting

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/notification"
)

const SchemaVersion = "phase-7-m2-eod-summary/v1"

var ErrInvalid = errors.New("invalid end-of-day summary")

type Money struct {
	Availability string `json:"availability"`
	Minor        int64  `json:"minor,omitempty"`
	Currency     string `json:"currency,omitempty"`
	Reason       string `json:"reason,omitempty"`
}
type Financial struct {
	Status           string `json:"status"`
	SnapshotChecksum string `json:"snapshot_checksum,omitempty"`
	RealizedPnL      Money  `json:"realized_pnl"`
	UnrealizedPnL    Money  `json:"unrealized_pnl"`
	TotalPnL         Money  `json:"total_pnl"`
	MaxDrawdown      Money  `json:"max_drawdown"`
}
type Counts struct {
	StrategiesActive         int64 `json:"strategies_active"`
	Proposals                int64 `json:"proposals"`
	RiskApproved             int64 `json:"risk_approved"`
	RiskModified             int64 `json:"risk_modified"`
	RiskRejected             int64 `json:"risk_rejected"`
	PaperExecutions          int64 `json:"paper_executions"`
	ShadowExecutions         int64 `json:"shadow_executions"`
	PartialFills             int64 `json:"partial_fills"`
	Fills                    int64 `json:"fills"`
	KillSwitches             int64 `json:"kill_switches"`
	CircuitBreakers          int64 `json:"circuit_breakers"`
	ReconciliationMismatches int64 `json:"reconciliation_mismatches"`
	UnknownExecutions        int64 `json:"unknown_executions"`
	CASRestrictions          int64 `json:"cas_restrictions"`
	CASEvidence              int64 `json:"cas_evidence"`
	ReadinessIncidents       int64 `json:"readiness_incidents"`
}
type Summary struct {
	SchemaVersion string    `json:"schema_version"`
	ID            string    `json:"id"`
	Checksum      string    `json:"checksum"`
	Mode          string    `json:"mode"`
	TradingDate   string    `json:"trading_date"`
	Counts        Counts    `json:"counts"`
	Financial     Financial `json:"financial"`
	GeneratedAt   time.Time `json:"generated_at"`
	Final         bool      `json:"final"`
}

func NewSummary(value Summary) (Summary, error) {
	if (value.Mode != "PAPER" && value.Mode != "SHADOW") || len(value.TradingDate) != 10 || value.GeneratedAt.IsZero() {
		return Summary{}, ErrInvalid
	}
	value.SchemaVersion = SchemaVersion
	if value.Financial.Status == "" {
		value.Financial.Status = "UNAVAILABLE"
	}
	if value.Financial.Status != "COMPLETE" && value.Financial.Status != "PARTIAL" && value.Financial.Status != "STALE" && value.Financial.Status != "UNAVAILABLE" {
		return Summary{}, ErrInvalid
	}
	for _, money := range []*Money{&value.Financial.RealizedPnL, &value.Financial.UnrealizedPnL, &value.Financial.TotalPnL, &value.Financial.MaxDrawdown} {
		if money.Availability == "" {
			money.Availability = "UNAVAILABLE"
			money.Reason = "NOT_OBSERVED"
		}
		if money.Availability == "KNOWN" {
			if money.Currency == "" {
				return Summary{}, ErrInvalid
			}
		} else if money.Availability != "UNAVAILABLE" || money.Reason == "" {
			return Summary{}, ErrInvalid
		}
	}
	if value.Financial.Status == "COMPLETE" {
		if value.Financial.RealizedPnL.Availability != "KNOWN" || value.Financial.UnrealizedPnL.Availability != "KNOWN" || value.Financial.TotalPnL.Availability != "KNOWN" {
			return Summary{}, ErrInvalid
		}
	} else if value.Financial.TotalPnL.Availability == "KNOWN" {
		value.Financial.TotalPnL = Money{Availability: "UNAVAILABLE", Reason: "INCOMPLETE_VALUATION"}
	}
	value.GeneratedAt = value.GeneratedAt.UTC()
	id := sha256.Sum256([]byte(SchemaVersion + "|" + value.Mode + "|" + value.TradingDate))
	value.ID = hex.EncodeToString(id[:])
	value.Checksum = ""
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	value.Checksum = hex.EncodeToString(sum[:])
	return value, nil
}

type key struct{ mode, date string }
type financialSeries struct {
	observed, hasComplete, complete bool
	peak, maxDrawdown               int64
	currency                        string
}
type Accumulator struct {
	mu        sync.RWMutex
	counts    map[key]Counts
	financial map[key]Financial
	series    map[key]financialSeries
	summaries []Summary
	capacity  int
}

func NewAccumulator(capacity int) (*Accumulator, error) {
	if capacity <= 0 || capacity > 366 {
		return nil, ErrInvalid
	}
	return &Accumulator{counts: map[key]Counts{}, financial: map[key]Financial{}, series: map[key]financialSeries{}, capacity: capacity}, nil
}
func (a *Accumulator) Observe(event notification.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	k := key{event.Mode, event.TradingDate}
	c := a.counts[k]
	switch event.Kind {
	case notification.KindStrategyActivated:
		c.StrategiesActive++
	case notification.KindProposalGenerated:
		c.Proposals++
	case notification.KindRiskApproved:
		c.RiskApproved++
	case notification.KindRiskModified:
		c.RiskModified++
	case notification.KindRiskRejected:
		c.RiskRejected++
	case notification.KindPaperSubmitted:
		c.PaperExecutions++
	case notification.KindShadowTrade:
		c.ShadowExecutions++
	case notification.KindPaperPartialFill:
		c.PartialFills++
	case notification.KindPaperFill:
		c.Fills++
	case notification.KindKillSwitch:
		c.KillSwitches++
	case notification.KindCircuitBreaker:
		c.CircuitBreakers++
	case notification.KindReconciliationMismatch, notification.KindBrokerOnlyExposure:
		c.ReconciliationMismatches++
	case notification.KindExecutionUnknown:
		c.UnknownExecutions++
	case notification.KindCASRestricted:
		c.CASRestrictions++
	case notification.KindReadinessLost:
		c.ReadinessIncidents++
	}
	a.counts[k] = c
}
func (a *Accumulator) RecordCASEvidence(mode, date string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	k := key{mode, date}
	c := a.counts[k]
	c.CASEvidence++
	a.counts[k] = c
}
func (a *Accumulator) UpdateFinancial(mode, date string, value Financial) {
	a.mu.Lock()
	defer a.mu.Unlock()
	k := key{mode, date}
	series := a.series[k]
	if !series.observed {
		series.observed = true
		series.complete = true
	}
	if value.Status == "COMPLETE" && value.TotalPnL.Availability == "KNOWN" {
		if !series.hasComplete {
			series.hasComplete = true
			series.peak = value.TotalPnL.Minor
			series.currency = value.TotalPnL.Currency
		} else if series.complete {
			if value.TotalPnL.Minor > series.peak {
				series.peak = value.TotalPnL.Minor
			}
			drawdown := series.peak - value.TotalPnL.Minor
			if drawdown > series.maxDrawdown {
				series.maxDrawdown = drawdown
			}
		}
	} else {
		series.complete = false
	}
	if series.hasComplete && series.complete {
		value.MaxDrawdown = Money{Availability: "KNOWN", Minor: series.maxDrawdown, Currency: series.currency}
	} else {
		value.MaxDrawdown = Money{Availability: "UNAVAILABLE", Reason: "INCOMPLETE_SERIES"}
	}
	a.series[k] = series
	a.financial[k] = value
}
func (a *Accumulator) Close(mode, date string, at time.Time) (Summary, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, v := range a.summaries {
		if v.Mode == mode && v.TradingDate == date {
			return v, nil
		}
	}
	k := key{mode, date}
	value, err := NewSummary(Summary{Mode: mode, TradingDate: date, Counts: a.counts[k], Financial: a.financial[k], GeneratedAt: at, Final: true})
	if err != nil {
		return Summary{}, err
	}
	if len(a.summaries) == a.capacity {
		copy(a.summaries, a.summaries[1:])
		a.summaries = a.summaries[:a.capacity-1]
	}
	a.summaries = append(a.summaries, value)
	return value, nil
}
func (a *Accumulator) Latest() (Summary, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.summaries) == 0 {
		return Summary{}, false
	}
	return a.summaries[len(a.summaries)-1], true
}
func (a *Accumulator) Checksums() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]string, len(a.summaries))
	for i, v := range a.summaries {
		out[i] = v.Checksum
	}
	sort.Strings(out)
	return out
}
