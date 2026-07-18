package opshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
	"github.com/bibhuyash/tradeedge/internal/strategy/runner"
	strategystorage "github.com/bibhuyash/tradeedge/internal/strategy/storage"
)

type Repository interface {
	strategystorage.DefinitionRegistry
	strategystorage.InstanceRepository
	strategystorage.CheckpointRepository
	strategystorage.EvaluationRecordRepository
	strategystorage.ObservationRepository
	strategystorage.TradeProposalRepository
}

type HealthSource interface {
	Health() (bool, int, int, []runner.Failure)
}

type Handler struct {
	repository Repository
	runner     HealthSource
	timeout    time.Duration
}

func New(repository Repository, runner HealthSource, timeout time.Duration) http.Handler {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return Handler{repository: repository, runner: runner, timeout: timeout}
}

func (handler Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		write(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	if handler.repository == nil {
		write(writer, http.StatusServiceUnavailable, map[string]string{"error": "repository_unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	limit, ok := parseLimit(request)
	if !ok {
		write(writer, http.StatusBadRequest, map[string]string{"error": "invalid_limit"})
		return
	}
	switch strings.TrimSuffix(request.URL.Path, "/") {
	case "/api/v1/strategy/definitions":
		values, err := handler.repository.Definitions(ctx)
		handler.respond(writer, values, err, limit)
	case "/api/v1/strategy/versions":
		definitionID, err := strategymodel.NewDefinitionID(
			request.URL.Query().Get("definition"),
		)
		if err != nil {
			write(writer, http.StatusBadRequest, map[string]string{"error": "invalid_definition"})
			return
		}
		values, repositoryErr := handler.repository.Versions(ctx, definitionID)
		items := make([]map[string]any, len(values))
		for index, value := range values {
			items[index] = map[string]any{
				"definition_id":          value.Manifest.DefinitionID,
				"version_id":             value.VersionID.String(),
				"implementation_version": value.Manifest.ImplementationVersion,
				"input_contract_version": value.Manifest.InputContractVersion,
			}
		}
		handler.respond(writer, items, repositoryErr, limit)
	case "/api/v1/strategy/instances":
		values, err := handler.repository.Instances(ctx)
		items := make([]map[string]any, len(values))
		for index, value := range values {
			items[index] = map[string]any{
				"id": value.ID(), "definition_id": value.DefinitionID(),
				"version_id": value.VersionID().String(), "revision_id": value.RevisionID().String(),
				"generation": value.Generation(), "lifecycle": value.Lifecycle(),
			}
		}
		handler.respond(writer, items, err, limit)
	case "/api/v1/strategy/checkpoints":
		id, valid := instanceID(request)
		if !valid {
			write(writer, http.StatusBadRequest, map[string]string{"error": "invalid_instance"})
			return
		}
		values, err := handler.repository.Checkpoints(ctx, id)
		items := make([]map[string]any, len(values))
		for index, value := range values {
			items[index] = map[string]any{
				"revision": value.Revision(), "checksum": value.Checksum().String(),
				"parent_checksum": value.ParentChecksum().String(), "state_schema": value.State().SchemaVersion(),
				"state_bytes": value.State().Size(), "evaluation_id": value.EvaluationID().String(),
			}
		}
		handler.respond(writer, items, err, limit)
	case "/api/v1/strategy/evaluations":
		id, valid := instanceID(request)
		if !valid {
			write(writer, http.StatusBadRequest, map[string]string{"error": "invalid_instance"})
			return
		}
		values, err := handler.repository.Evaluations(ctx, id)
		items := make([]map[string]any, len(values))
		for index, value := range values {
			items[index] = map[string]any{
				"evaluation_id": value.EvaluationID().String(), "frame_id": value.FrameID().String(),
				"logical_time": value.LogicalTime(), "result_kind": value.ResultKind(),
				"checkpoint_revision": value.CheckpointRevision(),
				"no_action_reason":    value.NoActionReason(),
				"observation_code":    value.ObservationCode(),
				"proposal_id":         value.ProposalID().String(),
			}
		}
		handler.respond(writer, items, err, limit)
	case "/api/v1/strategy/observations":
		id, valid := instanceID(request)
		if !valid {
			write(writer, http.StatusBadRequest, map[string]string{"error": "invalid_instance"})
			return
		}
		values, err := handler.repository.Observations(ctx, id)
		items := make([]map[string]any, len(values))
		for index, value := range values {
			items[index] = map[string]any{
				"evaluation_id": value.EvaluationID().String(), "generated_at": value.GeneratedAt(),
				"code": value.Draft().Code,
			}
		}
		handler.respond(writer, items, err, limit)
	case "/api/v1/strategy/proposals":
		id, valid := instanceID(request)
		if !valid {
			write(writer, http.StatusBadRequest, map[string]string{"error": "invalid_instance"})
			return
		}
		values, err := handler.repository.Proposals(ctx, id)
		items := make([]map[string]any, len(values))
		for index, value := range values {
			items[index] = map[string]any{
				"proposal_id":    value.ID().String(),
				"evaluation_id":  value.Metadata().EvaluationID.String(),
				"generated_at":   value.Metadata().GeneratedAt,
				"rationale_code": value.Draft().RationaleCode,
				"expires_at":     value.Draft().ExpiresAt, "leg_count": len(value.Draft().Legs),
			}
		}
		handler.respond(writer, items, err, limit)
	case "/api/v1/strategy/runner":
		if handler.runner == nil {
			write(writer, http.StatusServiceUnavailable, map[string]string{"error": "runner_unavailable"})
			return
		}
		closed, inFlight, keyed, failures := handler.runner.Health()
		if len(failures) > limit {
			failures = failures[len(failures)-limit:]
		}
		failureItems := make([]map[string]any, len(failures))
		for index, failure := range failures {
			failureItems[index] = map[string]any{
				"at": failure.At, "instance_id": failure.InstanceID,
				"outcome": failure.Outcome,
			}
		}
		write(writer, http.StatusOK, map[string]any{
			"closed": closed, "in_flight": inFlight, "keyed_instances": keyed,
			"recent_failures": failureItems,
		})
	default:
		write(writer, http.StatusNotFound, map[string]string{"error": "not_found"})
	}
}

func (handler Handler) respond(writer http.ResponseWriter, values any, err error, limit int) {
	if errors.Is(err, strategystorage.ErrNotFound) {
		write(writer, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	if err != nil {
		write(writer, http.StatusServiceUnavailable, map[string]string{"error": "repository_unavailable"})
		return
	}
	raw, _ := json.Marshal(values)
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) == nil && len(items) > limit {
		items = items[len(items)-limit:]
		write(writer, http.StatusOK, map[string]any{"items": items, "limit": limit})
		return
	}
	write(writer, http.StatusOK, map[string]any{"items": values, "limit": limit})
}

func parseLimit(request *http.Request) (int, bool) {
	if request.URL.Query().Get("limit") == "" {
		return 50, true
	}
	value, err := strconv.Atoi(request.URL.Query().Get("limit"))
	return value, err == nil && value > 0 && value <= 100
}

func instanceID(request *http.Request) (domain.StrategyID, bool) {
	value, err := domain.NewStrategyID(request.URL.Query().Get("instance"))
	return value, err == nil
}

func write(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
