package model

import (
	"errors"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

var ErrInvalidEvaluation = errors.New("invalid strategy evaluation")

type ResultKind string

const (
	ResultNoAction      ResultKind = "NO_ACTION"
	ResultObservation   ResultKind = "OBSERVATION"
	ResultTradeProposal ResultKind = "TRADE_PROPOSAL"
)

type NoActionReason string

const (
	NoActionInsufficientHistory   NoActionReason = "INSUFFICIENT_HISTORY"
	NoActionConditionsNotMet      NoActionReason = "CONDITIONS_NOT_MET"
	NoActionCooldownActive        NoActionReason = "COOLDOWN_ACTIVE"
	NoActionAlreadyPositioned     NoActionReason = "ALREADY_POSITIONED"
	NoActionStateTransitionOnly   NoActionReason = "STATE_TRANSITION_ONLY"
	NoActionDisabled              NoActionReason = "STRATEGY_DISABLED"
	NoActionStaleMarketData       NoActionReason = "STALE_MARKET_DATA"
	NoActionSessionNotAllowed     NoActionReason = "SESSION_NOT_ALLOWED"
	NoActionCASRestricted         NoActionReason = "CAS_RESTRICTED"
	NoActionPositionAlreadyOpen   NoActionReason = "POSITION_ALREADY_OPEN"
	NoActionNoCrossover           NoActionReason = "NO_CROSSOVER"
	NoActionMappingUnavailable    NoActionReason = "EXECUTION_MAPPING_UNAVAILABLE"
	NoActionAuthoritativeConflict NoActionReason = "AUTHORITATIVE_STATE_CONFLICT"
)

func (reason NoActionReason) Valid() bool {
	switch reason {
	case NoActionInsufficientHistory, NoActionConditionsNotMet, NoActionCooldownActive,
		NoActionAlreadyPositioned, NoActionStateTransitionOnly, NoActionDisabled,
		NoActionStaleMarketData, NoActionSessionNotAllowed, NoActionCASRestricted,
		NoActionPositionAlreadyOpen, NoActionNoCrossover, NoActionMappingUnavailable,
		NoActionAuthoritativeConflict:
		return true
	default:
		return false
	}
}

type NoActionDraft struct {
	Reason      NoActionReason
	Explanation string
}

type EvaluationResult struct {
	kind        ResultKind
	nextState   StrategyRuntimeState
	noAction    NoActionDraft
	observation ObservationDraft
	proposal    ProposalDraft
}

func NewNoActionResult(
	nextState StrategyRuntimeState,
	reason NoActionReason,
	explanation string,
) (EvaluationResult, error) {
	if nextState.IsZero() || !reason.Valid() ||
		strings.TrimSpace(explanation) == "" || len(explanation) > MaximumExplanationBytes {
		return EvaluationResult{}, ErrInvalidEvaluation
	}
	return EvaluationResult{
		kind: ResultNoAction, nextState: nextState,
		noAction: NoActionDraft{Reason: reason, Explanation: explanation},
	}, nil
}

func NewObservationResult(
	nextState StrategyRuntimeState,
	observation ObservationDraft,
) (EvaluationResult, error) {
	if nextState.IsZero() || observation.Validate() != nil {
		return EvaluationResult{}, ErrInvalidEvaluation
	}
	observation.Evidence = cloneEvidence(observation.Evidence)
	observation.ConfidenceBPS = cloneInt32(observation.ConfidenceBPS)
	return EvaluationResult{
		kind: ResultObservation, nextState: nextState, observation: observation,
	}, nil
}

func NewTradeProposalResult(
	nextState StrategyRuntimeState,
	proposal ProposalDraft,
) (EvaluationResult, error) {
	validated, err := NewProposalDraft(proposal)
	if nextState.IsZero() || err != nil {
		return EvaluationResult{}, ErrInvalidEvaluation
	}
	return EvaluationResult{
		kind: ResultTradeProposal, nextState: nextState, proposal: validated,
	}, nil
}

func (result EvaluationResult) Kind() ResultKind { return result.kind }
func (result EvaluationResult) NextState() StrategyRuntimeState {
	return result.nextState
}
func (result EvaluationResult) NoAction() (NoActionDraft, bool) {
	return result.noAction, result.kind == ResultNoAction
}
func (result EvaluationResult) Observation() (ObservationDraft, bool) {
	if result.kind != ResultObservation {
		return ObservationDraft{}, false
	}
	value := result.observation
	value.Evidence = cloneEvidence(value.Evidence)
	value.ConfidenceBPS = cloneInt32(value.ConfidenceBPS)
	return value, true
}
func (result EvaluationResult) Proposal() (ProposalDraft, bool) {
	if result.kind != ResultTradeProposal {
		return ProposalDraft{}, false
	}
	value := result.proposal
	value.Legs = append([]ProposalLeg(nil), value.Legs...)
	value.Evidence = cloneEvidence(value.Evidence)
	value.RiskHints = append([]RiskHint(nil), value.RiskHints...)
	value.ConfidenceBPS = cloneInt32(value.ConfidenceBPS)
	return value, true
}

type EntropySource interface {
	Uint64() uint64
}

type EvaluationContext struct {
	DefinitionID       DefinitionID
	VersionID          VersionID
	InstanceID         domain.StrategyID
	InstanceRevisionID InstanceRevisionID
	InstanceGeneration uint64
	Configuration      StrategyConfiguration
	EvaluationID       EvaluationID
	TriggerID          TriggerID
	LogicalTime        time.Time
	Frame              CandleFrame
	PriorState         StrategyRuntimeState
	Readiness          ReadinessEvidence
	Entropy            EntropySource
}

func (input EvaluationContext) Validate(descriptor Descriptor) error {
	manifest := descriptor.Manifest
	expectedRevision, revisionErr := NewInstanceRevisionID(
		input.InstanceID, input.VersionID, input.Configuration.Hash(), input.InstanceGeneration,
	)
	if descriptor.Validate() != nil || input.DefinitionID != manifest.DefinitionID ||
		input.VersionID != descriptor.VersionID ||
		revisionErr != nil || input.InstanceRevisionID != expectedRevision ||
		strings.TrimSpace(string(input.InstanceID)) == "" ||
		input.Configuration.IsZero() ||
		input.Configuration.SchemaVersion() != manifest.ConfigurationSchemaVersion ||
		input.EvaluationID.IsZero() || input.TriggerID.IsZero() ||
		input.LogicalTime.IsZero() || input.Frame.IsZero() ||
		!input.LogicalTime.Equal(input.Frame.LogicalTime()) ||
		input.TriggerID != input.Frame.TriggerID() ||
		input.Frame.Subscription().Version() != descriptor.Subscriptions.Version() ||
		input.Frame.CalendarVersion() != input.Readiness.CalendarVersion ||
		input.PriorState.IsZero() ||
		input.PriorState.SchemaVersion() != manifest.StateSchemaVersion ||
		!input.Readiness.Ready() || input.Entropy == nil {
		return ErrInvalidEvaluation
	}
	return nil
}
