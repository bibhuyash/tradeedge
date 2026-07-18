package model

import (
	"errors"
	"regexp"
	"strings"
	"time"

	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

const (
	MaximumEvidenceEntries  = 64
	MaximumExplanationBytes = 4096
)

var (
	ErrInvalidObservation = errors.New("invalid strategy observation")
	stableCodePattern     = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
)

type Evidence struct {
	Code           string
	SourceEventIDs []marketmodel.EventID
	Value          int64
	Unit           string
	Explanation    string
}

func (evidence Evidence) Validate() error {
	if !stableCodePattern.MatchString(evidence.Code) ||
		len(evidence.SourceEventIDs) == 0 ||
		strings.TrimSpace(evidence.Unit) == "" ||
		strings.TrimSpace(evidence.Explanation) == "" ||
		len(evidence.Explanation) > MaximumExplanationBytes {
		return ErrInvalidObservation
	}
	for _, eventID := range evidence.SourceEventIDs {
		if eventID.IsZero() {
			return ErrInvalidObservation
		}
	}
	return nil
}

type ObservationDraft struct {
	Code          string
	Explanation   string
	Evidence      []Evidence
	ConfidenceBPS *int32
}

func (observation ObservationDraft) Validate() error {
	if !stableCodePattern.MatchString(observation.Code) ||
		strings.TrimSpace(observation.Explanation) == "" ||
		len(observation.Explanation) > MaximumExplanationBytes ||
		len(observation.Evidence) == 0 ||
		len(observation.Evidence) > MaximumEvidenceEntries {
		return ErrInvalidObservation
	}
	if observation.ConfidenceBPS != nil &&
		(*observation.ConfidenceBPS < 0 || *observation.ConfidenceBPS > 10000) {
		return ErrInvalidObservation
	}
	for _, evidence := range observation.Evidence {
		if err := evidence.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type StrategyObservation struct {
	evaluationID EvaluationID
	generatedAt  time.Time
	draft        ObservationDraft
}

func NewStrategyObservation(
	evaluationID EvaluationID,
	generatedAt time.Time,
	draft ObservationDraft,
) (StrategyObservation, error) {
	if evaluationID.IsZero() || generatedAt.IsZero() || draft.Validate() != nil {
		return StrategyObservation{}, ErrInvalidObservation
	}
	draft.Evidence = cloneEvidence(draft.Evidence)
	draft.ConfidenceBPS = cloneInt32(draft.ConfidenceBPS)
	return StrategyObservation{
		evaluationID: evaluationID, generatedAt: generatedAt.UTC(), draft: draft,
	}, nil
}

func (observation StrategyObservation) EvaluationID() EvaluationID {
	return observation.evaluationID
}
func (observation StrategyObservation) GeneratedAt() time.Time {
	return observation.generatedAt
}
func (observation StrategyObservation) Draft() ObservationDraft {
	result := observation.draft
	result.Evidence = cloneEvidence(result.Evidence)
	result.ConfidenceBPS = cloneInt32(result.ConfidenceBPS)
	return result
}
