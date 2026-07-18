package storage

import (
	"crypto/sha256"
	"encoding/json"

	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

func CanonicalDefinition(record DefinitionRecord) ([]byte, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		ID            string `json:"id"`
		SchemaVersion string `json:"schema_version"`
		Name          string `json:"name"`
		Description   string `json:"description"`
	}{
		ID: record.ID.String(), SchemaVersion: record.SchemaVersion,
		Name: record.Name, Description: record.Description,
	})
}

func CanonicalDescriptor(descriptor strategymodel.Descriptor) ([]byte, error) {
	if err := descriptor.Validate(); err != nil {
		return nil, err
	}
	type subscriptionWire struct {
		Role         string `json:"role"`
		InstrumentID string `json:"instrument_id"`
		Interval     string `json:"interval"`
		Required     bool   `json:"required"`
		Trigger      bool   `json:"trigger"`
		Lookback     int    `json:"lookback"`
		MaximumAgeNS int64  `json:"maximum_age_ns"`
	}
	subscriptions := descriptor.Subscriptions.Subscriptions()
	wires := make([]subscriptionWire, len(subscriptions))
	for index, subscription := range subscriptions {
		wires[index] = subscriptionWire{
			Role: subscription.Role, InstrumentID: subscription.InstrumentID.String(),
			Interval: string(subscription.Interval), Required: subscription.Required,
			Trigger: subscription.Trigger, Lookback: subscription.Lookback,
			MaximumAgeNS: subscription.MaximumAge.Nanoseconds(),
		}
	}
	manifest := descriptor.Manifest
	return json.Marshal(struct {
		DefinitionID               string             `json:"definition_id"`
		VersionID                  string             `json:"version_id"`
		ImplementationVersion      string             `json:"implementation_version"`
		InputContractVersion       string             `json:"input_contract_version"`
		ConfigurationSchemaVersion string             `json:"configuration_schema_version"`
		StateSchemaVersion         string             `json:"state_schema_version"`
		ResultSchemaVersion        string             `json:"result_schema_version"`
		ProposalSchemaVersion      string             `json:"proposal_schema_version"`
		SubscriptionMode           string             `json:"subscription_mode"`
		SubscriptionVersion        string             `json:"subscription_version"`
		Subscriptions              []subscriptionWire `json:"subscriptions"`
	}{
		DefinitionID: manifest.DefinitionID.String(), VersionID: descriptor.VersionID.String(),
		ImplementationVersion:      manifest.ImplementationVersion,
		InputContractVersion:       manifest.InputContractVersion,
		ConfigurationSchemaVersion: manifest.ConfigurationSchemaVersion,
		StateSchemaVersion:         manifest.StateSchemaVersion,
		ResultSchemaVersion:        manifest.ResultSchemaVersion,
		ProposalSchemaVersion:      manifest.ProposalSchemaVersion,
		SubscriptionMode:           string(descriptor.Subscriptions.Mode()),
		SubscriptionVersion:        descriptor.Subscriptions.Version(), Subscriptions: wires,
	})
}

func CanonicalInstance(instance strategymodel.StrategyInstance) ([]byte, error) {
	configuration := instance.Configuration()
	return json.Marshal(struct {
		ID                  string          `json:"id"`
		DefinitionID        string          `json:"definition_id"`
		VersionID           string          `json:"version_id"`
		RevisionID          string          `json:"revision_id"`
		Generation          uint64          `json:"generation"`
		Lifecycle           string          `json:"lifecycle"`
		ConfigurationSchema string          `json:"configuration_schema"`
		ConfigurationHash   string          `json:"configuration_hash"`
		Configuration       json.RawMessage `json:"configuration"`
	}{
		ID: string(instance.ID()), DefinitionID: instance.DefinitionID().String(),
		VersionID: instance.VersionID().String(), RevisionID: instance.RevisionID().String(),
		Generation: instance.Generation(), Lifecycle: string(instance.Lifecycle()),
		ConfigurationSchema: configuration.SchemaVersion(),
		ConfigurationHash:   configuration.Hash().String(),
		Configuration:       json.RawMessage(configuration.CanonicalJSON()),
	})
}

type evidenceWire struct {
	Code           string   `json:"code"`
	SourceEventIDs []string `json:"source_event_ids"`
	Value          int64    `json:"value"`
	Unit           string   `json:"unit"`
	Explanation    string   `json:"explanation"`
}

func evidenceWires(evidence []strategymodel.Evidence) []evidenceWire {
	result := make([]evidenceWire, len(evidence))
	for index, item := range evidence {
		eventIDs := make([]string, len(item.SourceEventIDs))
		for eventIndex, eventID := range item.SourceEventIDs {
			eventIDs[eventIndex] = eventID.String()
		}
		result[index] = evidenceWire{
			Code: item.Code, SourceEventIDs: eventIDs, Value: item.Value,
			Unit: item.Unit, Explanation: item.Explanation,
		}
	}
	return result
}

func canonicalEvaluation(record strategymodel.EvaluationRecord) ([]byte, error) {
	return json.Marshal(struct {
		EvaluationID       string `json:"evaluation_id"`
		DefinitionID       string `json:"definition_id"`
		VersionID          string `json:"version_id"`
		InstanceID         string `json:"instance_id"`
		InstanceRevisionID string `json:"instance_revision_id"`
		ConfigurationHash  string `json:"configuration_hash"`
		FrameID            string `json:"frame_id"`
		LogicalTime        string `json:"logical_time"`
		ResultKind         string `json:"result_kind"`
		NoActionReason     string `json:"no_action_reason,omitempty"`
		PriorStateHash     string `json:"prior_state_hash"`
		NextStateHash      string `json:"next_state_hash"`
		CheckpointRevision uint64 `json:"checkpoint_revision"`
		ObservationCode    string `json:"observation_code,omitempty"`
		ProposalID         string `json:"proposal_id,omitempty"`
	}{
		EvaluationID: record.EvaluationID().String(),
		DefinitionID: record.DefinitionID().String(), VersionID: record.VersionID().String(),
		InstanceID:         string(record.InstanceID()),
		InstanceRevisionID: record.InstanceRevisionID().String(),
		ConfigurationHash:  record.ConfigurationHash().String(),
		FrameID:            record.FrameID().String(),
		LogicalTime:        record.LogicalTime().UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		ResultKind:         string(record.ResultKind()), NoActionReason: string(record.NoActionReason()),
		PriorStateHash:     record.PriorStateHash().String(),
		NextStateHash:      record.NextStateHash().String(),
		CheckpointRevision: record.CheckpointRevision(),
		ObservationCode:    record.ObservationCode(),
		ProposalID:         zeroableProposalID(record.ProposalID()),
	})
}

func canonicalObservation(observation strategymodel.StrategyObservation) ([]byte, error) {
	draft := observation.Draft()
	return json.Marshal(struct {
		EvaluationID string         `json:"evaluation_id"`
		GeneratedAt  string         `json:"generated_at"`
		Code         string         `json:"code"`
		Explanation  string         `json:"explanation"`
		Evidence     []evidenceWire `json:"evidence"`
		Confidence   *int32         `json:"confidence_bps,omitempty"`
	}{
		EvaluationID: observation.EvaluationID().String(),
		GeneratedAt:  observation.GeneratedAt().UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		Code:         draft.Code, Explanation: draft.Explanation,
		Evidence: evidenceWires(draft.Evidence), Confidence: draft.ConfidenceBPS,
	})
}

func canonicalProposal(proposal strategymodel.TradeProposal) ([]byte, error) {
	type legWire struct {
		InstrumentID    string `json:"instrument_id"`
		Side            string `json:"side"`
		Ratio           uint32 `json:"ratio"`
		PriceMinor      int64  `json:"reference_price_minor"`
		Currency        string `json:"currency"`
		MaxDeviationBPS int32  `json:"max_deviation_bps"`
	}
	type riskHintWire struct {
		Code  string `json:"code"`
		Value int64  `json:"value"`
		Unit  string `json:"unit"`
	}
	metadata := proposal.Metadata()
	draft := proposal.Draft()
	events := make([]string, len(metadata.SourceEventIDs))
	for index, eventID := range metadata.SourceEventIDs {
		events[index] = eventID.String()
	}
	instruments := make([]string, len(metadata.RequiredInstrumentIDs))
	for index, instrumentID := range metadata.RequiredInstrumentIDs {
		instruments[index] = instrumentID.String()
	}
	legs := make([]legWire, len(draft.Legs))
	for index, leg := range draft.Legs {
		legs[index] = legWire{
			InstrumentID: leg.InstrumentID.String(), Side: string(leg.Side), Ratio: leg.Ratio,
			PriceMinor:      leg.ReferencePrice.MinorUnits(),
			Currency:        leg.ReferencePrice.Currency().String(),
			MaxDeviationBPS: leg.MaxDeviationBPS,
		}
	}
	hints := make([]riskHintWire, len(draft.RiskHints))
	for index, hint := range draft.RiskHints {
		hints[index] = riskHintWire{Code: hint.Code, Value: hint.Value, Unit: hint.Unit}
	}
	return json.Marshal(struct {
		ProposalID            string         `json:"proposal_id"`
		DefinitionID          string         `json:"definition_id"`
		VersionID             string         `json:"version_id"`
		InstanceID            string         `json:"instance_id"`
		InstanceRevisionID    string         `json:"instance_revision_id"`
		EvaluationID          string         `json:"evaluation_id"`
		FrameID               string         `json:"frame_id"`
		GeneratedAt           string         `json:"generated_at"`
		SourceEventIDs        []string       `json:"source_event_ids"`
		RequiredInstrumentIDs []string       `json:"required_instrument_ids"`
		SchemaVersion         string         `json:"schema_version"`
		Legs                  []legWire      `json:"legs"`
		SizingKind            string         `json:"sizing_kind"`
		SizingValueBPS        int32          `json:"sizing_value_bps"`
		ValidFrom             string         `json:"valid_from"`
		ExpiresAt             string         `json:"expires_at"`
		RationaleCode         string         `json:"rationale_code"`
		Explanation           string         `json:"explanation"`
		Evidence              []evidenceWire `json:"evidence"`
		ConfidenceBPS         *int32         `json:"confidence_bps,omitempty"`
		RiskHints             []riskHintWire `json:"risk_hints"`
		ExitPolicyReference   string         `json:"exit_policy_reference"`
	}{
		ProposalID: proposal.ID().String(), DefinitionID: metadata.DefinitionID.String(),
		VersionID: metadata.VersionID.String(), InstanceID: string(metadata.InstanceID),
		InstanceRevisionID: metadata.InstanceRevisionID.String(),
		EvaluationID:       metadata.EvaluationID.String(), FrameID: metadata.FrameID.String(),
		GeneratedAt:    metadata.GeneratedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		SourceEventIDs: events, RequiredInstrumentIDs: instruments,
		SchemaVersion: draft.SchemaVersion, Legs: legs, SizingKind: string(draft.Sizing.Kind),
		SizingValueBPS: draft.Sizing.ValueBPS,
		ValidFrom:      draft.ValidFrom.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		ExpiresAt:      draft.ExpiresAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		RationaleCode:  draft.RationaleCode, Explanation: draft.Explanation,
		Evidence: evidenceWires(draft.Evidence), ConfidenceBPS: draft.ConfidenceBPS,
		RiskHints: hints, ExitPolicyReference: draft.ExitPolicyReference,
	})
}

func NewEvaluationPublication(
	input EvaluationPublication,
) (EvaluationPublication, error) {
	if err := validatePublication(input); err != nil {
		return EvaluationPublication{}, err
	}
	canonical, err := CanonicalPublication(input)
	if err != nil {
		return EvaluationPublication{}, ErrInvalidPublication
	}
	checksum := PublicationChecksum(sha256.Sum256(canonical))
	if !input.Checksum.IsZero() && input.Checksum != checksum {
		return EvaluationPublication{}, ErrInvalidPublication
	}
	input.Checksum = checksum
	return input, nil
}

func CanonicalPublication(publication EvaluationPublication) ([]byte, error) {
	checkpoint, err := EncodeCheckpoint(publication.Checkpoint)
	if err != nil {
		return nil, err
	}
	record, err := canonicalEvaluation(publication.Record)
	if err != nil {
		return nil, err
	}
	var observation, proposal json.RawMessage
	if publication.Observation != nil {
		encoded, encodeErr := canonicalObservation(*publication.Observation)
		if encodeErr != nil {
			return nil, encodeErr
		}
		observation = encoded
	}
	if publication.Proposal != nil {
		encoded, encodeErr := canonicalProposal(*publication.Proposal)
		if encodeErr != nil {
			return nil, encodeErr
		}
		proposal = encoded
	}
	return json.Marshal(struct {
		SchemaVersion         int             `json:"schema_version"`
		InstanceID            string          `json:"instance_id"`
		DefinitionID          string          `json:"definition_id"`
		VersionID             string          `json:"version_id"`
		InstanceRevisionID    string          `json:"instance_revision_id"`
		ConfigurationHash     string          `json:"configuration_hash"`
		EvaluationID          string          `json:"evaluation_id"`
		FrameID               string          `json:"frame_id"`
		ExpectedStateRevision uint64          `json:"expected_state_revision"`
		Checkpoint            json.RawMessage `json:"checkpoint"`
		Record                json.RawMessage `json:"record"`
		Observation           json.RawMessage `json:"observation,omitempty"`
		Proposal              json.RawMessage `json:"proposal,omitempty"`
	}{
		SchemaVersion: 1, InstanceID: string(publication.InstanceID),
		DefinitionID:       publication.DefinitionID.String(),
		VersionID:          publication.VersionID.String(),
		InstanceRevisionID: publication.InstanceRevisionID.String(),
		ConfigurationHash:  publication.ConfigurationHash.String(),
		EvaluationID:       publication.EvaluationID.String(), FrameID: publication.FrameID.String(),
		ExpectedStateRevision: publication.ExpectedStateRevision,
		Checkpoint:            checkpoint, Record: record, Observation: observation, Proposal: proposal,
	})
}

func validatePublication(publication EvaluationPublication) error {
	checkpoint := publication.Checkpoint
	record := publication.Record
	if publication.InstanceID == "" || publication.DefinitionID == "" ||
		publication.VersionID.IsZero() || publication.InstanceRevisionID.IsZero() ||
		publication.ConfigurationHash.IsZero() || publication.EvaluationID.IsZero() ||
		publication.FrameID.IsZero() || checkpoint.IsZero() || record.IsZero() ||
		checkpoint.InstanceID() != publication.InstanceID ||
		checkpoint.DefinitionID() != publication.DefinitionID ||
		checkpoint.VersionID() != publication.VersionID ||
		checkpoint.InstanceRevisionID() != publication.InstanceRevisionID ||
		checkpoint.ConfigurationHash() != publication.ConfigurationHash ||
		checkpoint.EvaluationID() != publication.EvaluationID ||
		checkpoint.Revision() != publication.ExpectedStateRevision+1 ||
		record.InstanceID() != publication.InstanceID ||
		record.DefinitionID() != publication.DefinitionID ||
		record.VersionID() != publication.VersionID ||
		record.InstanceRevisionID() != publication.InstanceRevisionID ||
		record.ConfigurationHash() != publication.ConfigurationHash ||
		record.EvaluationID() != publication.EvaluationID ||
		record.FrameID() != publication.FrameID ||
		record.CheckpointRevision() != checkpoint.Revision() ||
		record.NextStateHash() != checkpoint.State().Hash() {
		return ErrInvalidPublication
	}
	switch record.ResultKind() {
	case strategymodel.ResultNoAction:
		if publication.Observation != nil || publication.Proposal != nil {
			return ErrInvalidPublication
		}
	case strategymodel.ResultObservation:
		if publication.Observation == nil || publication.Proposal != nil ||
			publication.Observation.EvaluationID() != publication.EvaluationID ||
			publication.Observation.Draft().Code != record.ObservationCode() {
			return ErrInvalidPublication
		}
	case strategymodel.ResultTradeProposal:
		if publication.Proposal == nil || publication.Observation != nil ||
			publication.Proposal.ID() != record.ProposalID() {
			return ErrInvalidPublication
		}
		metadata := publication.Proposal.Metadata()
		if metadata.DefinitionID != publication.DefinitionID ||
			metadata.VersionID != publication.VersionID ||
			metadata.InstanceID != publication.InstanceID ||
			metadata.InstanceRevisionID != publication.InstanceRevisionID ||
			metadata.EvaluationID != publication.EvaluationID ||
			metadata.FrameID != publication.FrameID {
			return ErrInvalidPublication
		}
	default:
		return ErrInvalidPublication
	}
	return nil
}

func zeroableProposalID(value strategymodel.ProposalID) string {
	if value.IsZero() {
		return ""
	}
	return value.String()
}
