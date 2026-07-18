package model

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

const (
	MaximumProposalLegs     = 8
	MaximumRiskHints        = 8
	MaximumProposalValidity = 15 * time.Minute
)

var ErrInvalidTradeProposal = errors.New("invalid trade proposal")

type SizingIntentKind string

const SizingStrategyBudgetBPS SizingIntentKind = "STRATEGY_BUDGET_BPS"

type SizingIntent struct {
	Kind     SizingIntentKind
	ValueBPS int32
}

func (intent SizingIntent) Validate() error {
	if intent.Kind != SizingStrategyBudgetBPS ||
		intent.ValueBPS <= 0 || intent.ValueBPS > 10000 {
		return ErrInvalidTradeProposal
	}
	return nil
}

type ProposalLeg struct {
	InstrumentID    domain.InstrumentID
	Side            domain.Side
	Ratio           uint32
	ReferencePrice  domain.Price
	MaxDeviationBPS int32
}

func (leg ProposalLeg) Validate() error {
	if leg.InstrumentID.IsZero() ||
		(leg.Side != domain.SideBuy && leg.Side != domain.SideSell) ||
		leg.Ratio == 0 || leg.ReferencePrice.IsZeroValue() ||
		leg.MaxDeviationBPS < 0 || leg.MaxDeviationBPS > 10000 {
		return ErrInvalidTradeProposal
	}
	return nil
}

type RiskHint struct {
	Code  string
	Value int64
	Unit  string
}

func (hint RiskHint) Validate() error {
	if !stableCodePattern.MatchString(hint.Code) || strings.TrimSpace(hint.Unit) == "" {
		return ErrInvalidTradeProposal
	}
	return nil
}

type ProposalDraft struct {
	SchemaVersion       string
	Legs                []ProposalLeg
	Sizing              SizingIntent
	ValidFrom           time.Time
	ExpiresAt           time.Time
	RationaleCode       string
	Explanation         string
	Evidence            []Evidence
	ConfidenceBPS       *int32
	RiskHints           []RiskHint
	ExitPolicyReference string
}

func NewProposalDraft(input ProposalDraft) (ProposalDraft, error) {
	if strings.TrimSpace(input.SchemaVersion) == "" ||
		len(input.Legs) == 0 || len(input.Legs) > MaximumProposalLegs ||
		input.Sizing.Validate() != nil ||
		input.ValidFrom.IsZero() || !input.ExpiresAt.After(input.ValidFrom) ||
		input.ExpiresAt.Sub(input.ValidFrom) > MaximumProposalValidity ||
		!stableCodePattern.MatchString(input.RationaleCode) ||
		strings.TrimSpace(input.Explanation) == "" ||
		len(input.Explanation) > MaximumExplanationBytes ||
		len(input.Evidence) == 0 || len(input.Evidence) > MaximumEvidenceEntries ||
		len(input.RiskHints) > MaximumRiskHints ||
		strings.TrimSpace(input.ExitPolicyReference) == "" {
		return ProposalDraft{}, ErrInvalidTradeProposal
	}
	if input.ConfidenceBPS != nil &&
		(*input.ConfidenceBPS < 0 || *input.ConfidenceBPS > 10000) {
		return ProposalDraft{}, ErrInvalidTradeProposal
	}
	legs := append([]ProposalLeg(nil), input.Legs...)
	seen := make(map[domain.InstrumentID]struct{}, len(legs))
	var divisor uint32
	for _, leg := range legs {
		if err := leg.Validate(); err != nil {
			return ProposalDraft{}, err
		}
		if _, exists := seen[leg.InstrumentID]; exists {
			return ProposalDraft{}, ErrInvalidTradeProposal
		}
		seen[leg.InstrumentID] = struct{}{}
		divisor = greatestCommonDivisor(divisor, leg.Ratio)
	}
	for index := range legs {
		legs[index].Ratio /= divisor
	}
	sort.Slice(legs, func(i, j int) bool {
		left := legs[i].InstrumentID.String() + "|" + string(legs[i].Side)
		right := legs[j].InstrumentID.String() + "|" + string(legs[j].Side)
		return left < right
	})
	evidence := cloneEvidence(input.Evidence)
	for _, item := range evidence {
		if err := item.Validate(); err != nil {
			return ProposalDraft{}, err
		}
	}
	hints := append([]RiskHint(nil), input.RiskHints...)
	hintKeys := make(map[string]struct{}, len(hints))
	for _, hint := range hints {
		if err := hint.Validate(); err != nil {
			return ProposalDraft{}, err
		}
		key := hint.Code + "|" + hint.Unit
		if _, exists := hintKeys[key]; exists {
			return ProposalDraft{}, ErrInvalidTradeProposal
		}
		hintKeys[key] = struct{}{}
	}
	sort.Slice(hints, func(i, j int) bool {
		return hints[i].Code+"|"+hints[i].Unit < hints[j].Code+"|"+hints[j].Unit
	})
	result := input
	result.SchemaVersion = strings.TrimSpace(input.SchemaVersion)
	result.ValidFrom = input.ValidFrom.UTC()
	result.ExpiresAt = input.ExpiresAt.UTC()
	result.Legs = legs
	result.Evidence = evidence
	result.RiskHints = hints
	result.ConfidenceBPS = cloneInt32(input.ConfidenceBPS)
	return result, nil
}

type ProposalMetadata struct {
	DefinitionID          DefinitionID
	VersionID             VersionID
	InstanceID            domain.StrategyID
	InstanceRevisionID    InstanceRevisionID
	EvaluationID          EvaluationID
	FrameID               FrameID
	GeneratedAt           time.Time
	SourceEventIDs        []marketmodel.EventID
	RequiredInstrumentIDs []domain.InstrumentID
}

type TradeProposal struct {
	id       ProposalID
	metadata ProposalMetadata
	draft    ProposalDraft
}

func NewTradeProposal(metadata ProposalMetadata, draft ProposalDraft) (TradeProposal, error) {
	validated, err := NewProposalDraft(draft)
	_, definitionErr := NewDefinitionID(metadata.DefinitionID.String())
	if err != nil || definitionErr != nil || metadata.VersionID.IsZero() ||
		strings.TrimSpace(string(metadata.InstanceID)) == "" ||
		metadata.InstanceRevisionID.IsZero() || metadata.EvaluationID.IsZero() ||
		metadata.FrameID.IsZero() || metadata.GeneratedAt.IsZero() ||
		len(metadata.SourceEventIDs) == 0 || len(metadata.RequiredInstrumentIDs) == 0 {
		return TradeProposal{}, ErrInvalidTradeProposal
	}
	sourceIDs := append([]marketmodel.EventID(nil), metadata.SourceEventIDs...)
	sourceSeen := make(map[marketmodel.EventID]struct{}, len(sourceIDs))
	for _, id := range sourceIDs {
		if id.IsZero() {
			return TradeProposal{}, ErrInvalidTradeProposal
		}
		if _, exists := sourceSeen[id]; exists {
			return TradeProposal{}, ErrInvalidTradeProposal
		}
		sourceSeen[id] = struct{}{}
	}
	sort.Slice(sourceIDs, func(i, j int) bool { return sourceIDs[i].String() < sourceIDs[j].String() })
	instruments := append([]domain.InstrumentID(nil), metadata.RequiredInstrumentIDs...)
	instrumentSeen := make(map[domain.InstrumentID]struct{}, len(instruments))
	for _, id := range instruments {
		if id.IsZero() {
			return TradeProposal{}, ErrInvalidTradeProposal
		}
		if _, exists := instrumentSeen[id]; exists {
			return TradeProposal{}, ErrInvalidTradeProposal
		}
		instrumentSeen[id] = struct{}{}
	}
	sort.Slice(instruments, func(i, j int) bool { return instruments[i].String() < instruments[j].String() })
	parts := []string{
		metadata.DefinitionID.String(), metadata.VersionID.String(), string(metadata.InstanceID),
		metadata.InstanceRevisionID.String(), metadata.EvaluationID.String(), metadata.FrameID.String(),
		metadata.GeneratedAt.UTC().Format(time.RFC3339Nano),
	}
	for _, id := range sourceIDs {
		parts = append(parts, "event", id.String())
	}
	for _, id := range instruments {
		parts = append(parts, "instrument", id.String())
	}
	parts = append(parts,
		validated.SchemaVersion, validated.ValidFrom.Format(time.RFC3339Nano),
		validated.ExpiresAt.Format(time.RFC3339Nano), string(validated.Sizing.Kind),
		fmt.Sprint(validated.Sizing.ValueBPS), validated.RationaleCode,
		validated.ExitPolicyReference, validated.Explanation,
	)
	if validated.ConfidenceBPS != nil {
		parts = append(parts, "confidence", fmt.Sprint(*validated.ConfidenceBPS))
	}
	for _, leg := range validated.Legs {
		parts = append(parts, "leg", leg.InstrumentID.String(), string(leg.Side),
			fmt.Sprint(leg.Ratio), fmt.Sprint(leg.ReferencePrice.MinorUnits()),
			leg.ReferencePrice.Currency().String(), fmt.Sprint(leg.MaxDeviationBPS))
	}
	for _, item := range validated.Evidence {
		parts = append(parts, "evidence", item.Code, fmt.Sprint(item.Value), item.Unit, item.Explanation)
		for _, eventID := range item.SourceEventIDs {
			parts = append(parts, "evidence-event", eventID.String())
		}
	}
	for _, hint := range validated.RiskHints {
		parts = append(parts, "risk-hint", hint.Code, fmt.Sprint(hint.Value), hint.Unit)
	}
	key := stableKey("strategy-trade-proposal/v1", parts...)
	id, _ := NewProposalID(key)
	metadata.GeneratedAt = metadata.GeneratedAt.UTC()
	metadata.SourceEventIDs = sourceIDs
	metadata.RequiredInstrumentIDs = instruments
	return TradeProposal{id: id, metadata: metadata, draft: validated}, nil
}

func (proposal TradeProposal) ID() ProposalID { return proposal.id }
func (proposal TradeProposal) DeduplicationKey() string {
	return proposal.id.String()
}
func (proposal TradeProposal) Metadata() ProposalMetadata {
	result := proposal.metadata
	result.SourceEventIDs = append([]marketmodel.EventID(nil), result.SourceEventIDs...)
	result.RequiredInstrumentIDs = append([]domain.InstrumentID(nil), result.RequiredInstrumentIDs...)
	return result
}
func (proposal TradeProposal) Draft() ProposalDraft {
	result := proposal.draft
	result.Legs = append([]ProposalLeg(nil), result.Legs...)
	result.Evidence = cloneEvidence(result.Evidence)
	result.RiskHints = append([]RiskHint(nil), result.RiskHints...)
	result.ConfidenceBPS = cloneInt32(result.ConfidenceBPS)
	return result
}
func (proposal TradeProposal) IsZero() bool { return proposal.id.IsZero() }

func greatestCommonDivisor(left, right uint32) uint32 {
	if left == 0 {
		return right
	}
	for right != 0 {
		left, right = right, left%right
	}
	return left
}

func cloneEvidence(values []Evidence) []Evidence {
	result := append([]Evidence(nil), values...)
	for index := range result {
		result[index].SourceEventIDs = append([]marketmodel.EventID(nil), result[index].SourceEventIDs...)
	}
	return result
}

func cloneInt32(value *int32) *int32 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
