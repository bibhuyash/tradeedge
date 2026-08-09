// Package cas captures observational closing-auction evidence. It has no
// strategy, risk, execution, or session mutation capability.
package cas

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

const SchemaVersion = "phase-7-m2-cas-evidence/v1"

var ErrInvalid = errors.New("invalid CAS evidence")

type Availability string

const (
	Known       Availability = "KNOWN"
	Unavailable Availability = "UNAVAILABLE"
)

type Value struct {
	Availability  Availability `json:"availability"`
	Value         string       `json:"value,omitempty"`
	Reason        string       `json:"reason,omitempty"`
	SourceEventID string       `json:"source_event_id,omitempty"`
	ObservedAt    time.Time    `json:"observed_at,omitempty"`
}

func KnownValue(value, source string, at time.Time) Value {
	return Value{Availability: Known, Value: value, SourceEventID: source, ObservedAt: at.UTC()}
}
func UnavailableValue(reason string) Value { return Value{Availability: Unavailable, Reason: reason} }

type Price struct {
	Availability  Availability `json:"availability"`
	Minor         int64        `json:"minor,omitempty"`
	Currency      string       `json:"currency,omitempty"`
	Provenance    string       `json:"provenance,omitempty"`
	SourceEventID string       `json:"source_event_id,omitempty"`
	ObservedAt    time.Time    `json:"observed_at,omitempty"`
	Reason        string       `json:"reason,omitempty"`
}

func KnownPrice(minor int64, currency, provenance, source string, at time.Time) Price {
	return Price{Availability: Known, Minor: minor, Currency: currency, Provenance: provenance, SourceEventID: source, ObservedAt: at.UTC()}
}
func UnavailablePrice(reason string) Price { return Price{Availability: Unavailable, Reason: reason} }

type Record struct {
	SchemaVersion         string    `json:"schema_version"`
	ID                    string    `json:"id"`
	Checksum              string    `json:"checksum"`
	TradingDate           string    `json:"trading_date"`
	InstrumentID          string    `json:"instrument_id"`
	StrategyID            string    `json:"strategy_id"`
	RuntimeMode           string    `json:"runtime_mode"`
	Regime                string    `json:"regime"`
	ConfigurationVersion  string    `json:"configuration_version"`
	ConfigurationChecksum string    `json:"configuration_checksum"`
	CalendarVersion       string    `json:"calendar_version"`
	PreCAS                Price     `json:"pre_cas"`
	LatestEligibleLTP     Price     `json:"latest_eligible_ltp"`
	CASIndicative         Price     `json:"cas_indicative"`
	CASReference          Price     `json:"cas_reference"`
	CASEquilibrium        Price     `json:"cas_equilibrium"`
	OfficialClose         Price     `json:"official_close"`
	FuturesReference      Price     `json:"futures_reference"`
	IndexReference        Price     `json:"index_reference"`
	StrategyEligibility   Value     `json:"strategy_eligibility"`
	Proposal              Value     `json:"proposal"`
	RiskOutcome           Value     `json:"risk_outcome"`
	ShadowDecision        Value     `json:"shadow_decision"`
	PaperExecution        Value     `json:"paper_execution"`
	PositionBefore        Value     `json:"position_before"`
	PositionAfter         Value     `json:"position_after"`
	BlockedReason         Value     `json:"blocked_reason"`
	Readiness             Value     `json:"readiness"`
	SourceChecksums       []string  `json:"source_checksums"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type Spec = Record

func NewRecord(spec Spec) (Record, error) {
	spec.SchemaVersion = SchemaVersion
	spec.TradingDate = strings.TrimSpace(spec.TradingDate)
	spec.InstrumentID = strings.TrimSpace(spec.InstrumentID)
	spec.StrategyID = strings.TrimSpace(spec.StrategyID)
	spec.RuntimeMode = strings.TrimSpace(spec.RuntimeMode)
	if len(spec.TradingDate) != 10 || spec.InstrumentID == "" || spec.StrategyID == "" || (spec.RuntimeMode != "PAPER" && spec.RuntimeMode != "SHADOW") || (spec.Regime != "PRE_CAS" && spec.Regime != "CAS_ACTIVE" && spec.Regime != "POST_CAS") || strings.TrimSpace(spec.ConfigurationVersion) == "" || strings.TrimSpace(spec.ConfigurationChecksum) == "" || strings.TrimSpace(spec.CalendarVersion) == "" || spec.UpdatedAt.IsZero() {
		return Record{}, ErrInvalid
	}
	prices := []*Price{&spec.PreCAS, &spec.LatestEligibleLTP, &spec.CASIndicative, &spec.CASReference, &spec.CASEquilibrium, &spec.OfficialClose, &spec.FuturesReference, &spec.IndexReference}
	for _, value := range prices {
		if err := normalizePrice(value); err != nil {
			return Record{}, err
		}
	}
	values := []*Value{&spec.StrategyEligibility, &spec.Proposal, &spec.RiskOutcome, &spec.ShadowDecision, &spec.PaperExecution, &spec.PositionBefore, &spec.PositionAfter, &spec.BlockedReason, &spec.Readiness}
	for _, value := range values {
		if err := normalizeValue(value); err != nil {
			return Record{}, err
		}
	}
	spec.UpdatedAt = spec.UpdatedAt.UTC()
	sort.Strings(spec.SourceChecksums)
	spec.SourceChecksums = dedupe(spec.SourceChecksums)
	idSum := sha256.Sum256([]byte(SchemaVersion + "|" + spec.TradingDate + "|" + spec.InstrumentID + "|" + spec.StrategyID + "|" + spec.RuntimeMode))
	spec.ID = hex.EncodeToString(idSum[:])
	spec.Checksum = ""
	raw, err := json.Marshal(spec)
	if err != nil {
		return Record{}, ErrInvalid
	}
	sum := sha256.Sum256(raw)
	spec.Checksum = hex.EncodeToString(sum[:])
	return spec, nil
}
func normalizePrice(value *Price) error {
	if value.Availability == "" {
		*value = UnavailablePrice("NOT_OBSERVED")
	}
	if value.Availability == Known {
		if value.Minor <= 0 || value.Currency == "" || value.Provenance == "" || value.SourceEventID == "" || value.ObservedAt.IsZero() {
			return ErrInvalid
		}
		value.ObservedAt = value.ObservedAt.UTC()
		return nil
	}
	if value.Availability != Unavailable || value.Reason == "" {
		return ErrInvalid
	}
	return nil
}
func normalizeValue(value *Value) error {
	if value.Availability == "" {
		*value = UnavailableValue("NOT_OBSERVED")
	}
	if value.Availability == Known {
		if value.Value == "" || value.SourceEventID == "" || value.ObservedAt.IsZero() {
			return ErrInvalid
		}
		value.ObservedAt = value.ObservedAt.UTC()
		return nil
	}
	if value.Availability != Unavailable || value.Reason == "" {
		return ErrInvalid
	}
	return nil
}
func dedupe(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := values[:1]
	for _, v := range values[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

type Store struct {
	mu       sync.RWMutex
	capacity int
	records  []Record
	byID     map[string]int
}

func NewStore(capacity int) (*Store, error) {
	if capacity <= 0 || capacity > 10000 {
		return nil, ErrInvalid
	}
	return &Store{capacity: capacity, byID: map[string]int{}}, nil
}
func (s *Store) Put(value Record) error {
	if value.ID == "" || value.Checksum == "" {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if index, ok := s.byID[value.ID]; ok {
		s.records[index] = value
		return nil
	}
	if len(s.records) == s.capacity {
		delete(s.byID, s.records[0].ID)
		copy(s.records, s.records[1:])
		s.records = s.records[:s.capacity-1]
		for i := range s.records {
			s.byID[s.records[i].ID] = i
		}
	}
	s.byID[value.ID] = len(s.records)
	s.records = append(s.records, value)
	return nil
}
func (s *Store) Recent(limit int) []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if limit > len(s.records) {
		limit = len(s.records)
	}
	out := make([]Record, limit)
	for i := 0; i < limit; i++ {
		out[i] = s.records[len(s.records)-1-i]
	}
	return out
}

type Recorder struct {
	store *Store
	mu    sync.Mutex
}

func NewRecorder(store *Store) (*Recorder, error) {
	if store == nil {
		return nil, ErrInvalid
	}
	return &Recorder{store: store}, nil
}

// Capture atomically validates and stores a complete or explicitly unavailable
// observational record. Callers compose it only from committed source values.
func (r *Recorder) Capture(spec Spec) (Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, err := NewRecord(spec)
	if err != nil {
		return Record{}, err
	}
	if err = r.store.Put(value); err != nil {
		return Record{}, err
	}
	return value, nil
}
