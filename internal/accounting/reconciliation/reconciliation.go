package reconciliation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	accountingstorage "github.com/bibhuyash/tradeedge/internal/accounting/storage"
	"github.com/bibhuyash/tradeedge/internal/domain"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
)

var (
	ErrInvalidObservation  = errors.New("invalid broker position observation")
	ErrInvalidRequest      = errors.New("invalid position reconciliation request")
	ErrIdentityCollision   = errors.New("reconciliation identity collision")
	ErrDuplicateInProgress = errors.New("reconciliation already in progress")
	ErrPositionChanged     = errors.New("position changed during reconciliation")
	ErrShutdown            = errors.New("reconciliation coordinator is shut down")
)

type Classification string

const (
	Match                  Classification = "MATCH"
	QuantityMismatch       Classification = "QUANTITY_MISMATCH"
	DirectionMismatch      Classification = "DIRECTION_MISMATCH"
	LocalOnly              Classification = "LOCAL_ONLY"
	BrokerOnly             Classification = "BROKER_ONLY"
	StaleBrokerObservation Classification = "STALE_BROKER_OBSERVATION"
	Unknown                Classification = "UNKNOWN"
)

type Severity string

const (
	SeverityHealthy  Severity = "HEALTHY"
	SeverityHigh     Severity = "HIGH"
	SeverityCritical Severity = "CRITICAL"
)

type Direction string

const (
	DirectionEqual    Direction = "EQUAL"
	DirectionSame     Direction = "SAME"
	DirectionOpposite Direction = "OPPOSITE"
	DirectionUnknown  Direction = "UNKNOWN"
)

type ObservationScope string

const (
	ScopePaper ObservationScope = "PAPER"
	ScopeReal  ObservationScope = "REAL"
)

type RuntimeMode string

const (
	ModeOffline      RuntimeMode = "OFFLINE"
	ModePaper        RuntimeMode = "PAPER"
	ModeShadow       RuntimeMode = "SHADOW"
	ModeLiveDisabled RuntimeMode = "LIVE_DISABLED"
)

type BrokerPositionObservation struct {
	ID                   accountingmodel.StateChecksum
	Provider             string
	Binding              accountingmodel.AccountBinding
	InstrumentID         domain.InstrumentID
	NetQuantity          int64
	AveragePrice         *domain.Price
	AveragePriceReliable bool
	BrokerObservedAt     time.Time
	IngestedAt           time.Time
	SnapshotID           string
	SnapshotVersion      string
	Complete             bool
	Available            bool
	Scope                ObservationScope
	SourceChecksum       accountingmodel.StateChecksum
	canonical            []byte
}

type ObservationSpec struct {
	Provider             string
	Binding              accountingmodel.AccountBinding
	InstrumentID         domain.InstrumentID
	NetQuantity          int64
	AveragePrice         *domain.Price
	AveragePriceReliable bool
	BrokerObservedAt     time.Time
	IngestedAt           time.Time
	SnapshotID           string
	SnapshotVersion      string
	Complete             bool
	Available            bool
	Scope                ObservationScope
	SourceChecksum       accountingmodel.StateChecksum
}

func NewBrokerPositionObservation(spec ObservationSpec) (BrokerPositionObservation, error) {
	spec.Provider = strings.TrimSpace(spec.Provider)
	spec.SnapshotID = strings.TrimSpace(spec.SnapshotID)
	spec.SnapshotVersion = strings.TrimSpace(spec.SnapshotVersion)
	if spec.Provider == "" || spec.Binding.Validate() != nil || spec.InstrumentID.IsZero() || spec.NetQuantity == math.MinInt64 || spec.BrokerObservedAt.IsZero() || spec.IngestedAt.IsZero() || spec.SnapshotID == "" || spec.SnapshotVersion == "" || spec.SourceChecksum.IsZero() || (spec.Scope != ScopePaper && spec.Scope != ScopeReal) || spec.IngestedAt.Before(spec.BrokerObservedAt) {
		return BrokerPositionObservation{}, ErrInvalidObservation
	}
	if !spec.Available && (spec.NetQuantity != 0 || spec.AveragePrice != nil || spec.AveragePriceReliable) {
		return BrokerPositionObservation{}, ErrInvalidObservation
	}
	priceMinor := int64(0)
	currency := ""
	hasPrice := spec.AveragePrice != nil
	if hasPrice {
		if spec.AveragePrice.IsZeroValue() || spec.AveragePrice.MinorUnits() < 0 {
			return BrokerPositionObservation{}, ErrInvalidObservation
		}
		priceMinor = spec.AveragePrice.MinorUnits()
		currency = spec.AveragePrice.Currency().String()
	}
	if spec.AveragePriceReliable && !hasPrice {
		return BrokerPositionObservation{}, ErrInvalidObservation
	}
	raw, _ := json.Marshal(struct {
		Provider, PortfolioID, AccountID, BindingVersion, BindingChecksum, InstrumentID, SnapshotID, SnapshotVersion, Scope, SourceChecksum, BrokerObservedAt, IngestedAt, Currency string
		NetQuantity, AveragePriceMinor                                                                                                                                              int64
		HasAveragePrice, AveragePriceReliable, Complete, Available                                                                                                                  bool
	}{spec.Provider, spec.Binding.PortfolioID.String(), string(spec.Binding.AccountID), spec.Binding.Version, spec.Binding.Checksum().String(), spec.InstrumentID.String(), spec.SnapshotID, spec.SnapshotVersion, string(spec.Scope), spec.SourceChecksum.String(), spec.BrokerObservedAt.UTC().Format(time.RFC3339Nano), spec.IngestedAt.UTC().Format(time.RFC3339Nano), currency, spec.NetQuantity, priceMinor, hasPrice, spec.AveragePriceReliable, spec.Complete, spec.Available})
	id, _ := accountingmodel.NewStateChecksum("broker-position-observation/v1", raw)
	return BrokerPositionObservation{ID: id, Provider: spec.Provider, Binding: spec.Binding, InstrumentID: spec.InstrumentID, NetQuantity: spec.NetQuantity, AveragePrice: spec.AveragePrice, AveragePriceReliable: spec.AveragePriceReliable, BrokerObservedAt: spec.BrokerObservedAt.UTC(), IngestedAt: spec.IngestedAt.UTC(), SnapshotID: spec.SnapshotID, SnapshotVersion: spec.SnapshotVersion, Complete: spec.Complete, Available: spec.Available, Scope: spec.Scope, SourceChecksum: spec.SourceChecksum, canonical: raw}, nil
}
func (value BrokerPositionObservation) CanonicalJSON() []byte {
	return append([]byte(nil), value.canonical...)
}
func (value BrokerPositionObservation) IsZero() bool { return value.ID.IsZero() }

type Evidence struct {
	ID                  accountingmodel.StateChecksum
	ObservationID       accountingmodel.StateChecksum
	PositionID          accountingmodel.PositionID
	PositionRevision    accountingmodel.PositionRevision
	PositionChecksum    accountingmodel.StateChecksum
	Classification      Classification
	Severity            Severity
	LocalQuantity       int64
	BrokerQuantity      int64
	BrokerQuantityKnown bool
	Direction           Direction
	Fresh               bool
	Blocked             bool
	Reason              string
	EvaluatedAt         time.Time
	PolicyVersion       string
	canonical           []byte
}

func (value Evidence) CanonicalJSON() []byte { return append([]byte(nil), value.canonical...) }

type Checkpoint struct {
	ObservationID, EvidenceID, Checksum accountingmodel.StateChecksum
	canonical                           []byte
}

func NewCheckpoint(observationID, evidenceID accountingmodel.StateChecksum) (Checkpoint, error) {
	if observationID.IsZero() || evidenceID.IsZero() {
		return Checkpoint{}, ErrInvalidRequest
	}
	raw, _ := json.Marshal(struct{ ObservationID, EvidenceID string }{observationID.String(), evidenceID.String()})
	sum, _ := accountingmodel.NewStateChecksum("position-reconciliation-checkpoint/v1", raw)
	return Checkpoint{observationID, evidenceID, sum, raw}, nil
}

type PositionReader interface {
	Position(context.Context, accountingmodel.PositionID) (accountingmodel.PositionSnapshot, error)
}
type AccountBindingResolver interface {
	Resolve(context.Context, portfoliomodel.PortfolioID, time.Time) (accountingmodel.AccountBinding, error)
}
type Repository interface {
	Evidence(context.Context, accountingmodel.StateChecksum) (Evidence, error)
	Publish(context.Context, Evidence, Checkpoint) (Evidence, error)
}
type Config struct {
	MaxConcurrency      int
	Timeout, MaximumAge time.Duration
	PolicyVersion       string
}

func DefaultConfig() Config {
	return Config{4, time.Second, 30 * time.Second, "position-reconciliation/v1"}
}

type Request struct {
	Observation BrokerPositionObservation
	Mode        RuntimeMode
	EvaluatedAt time.Time
}

type Coordinator struct {
	positions  PositionReader
	repository Repository
	bindings   AccountBindingResolver
	config     Config
	semaphore  chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	running    map[accountingmodel.StateChecksum]struct{}
	closed     bool
	wait       sync.WaitGroup
	stopOnce   sync.Once
	stopped    chan struct{}
}

func New(positions PositionReader, repository Repository, bindings AccountBindingResolver, config Config) (*Coordinator, error) {
	if positions == nil || repository == nil || bindings == nil || config.MaxConcurrency <= 0 || config.MaxConcurrency > 64 || config.Timeout <= 0 || config.Timeout > time.Minute || config.MaximumAge <= 0 || strings.TrimSpace(config.PolicyVersion) == "" {
		return nil, ErrInvalidRequest
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Coordinator{positions: positions, repository: repository, bindings: bindings, config: config, semaphore: make(chan struct{}, config.MaxConcurrency), ctx: ctx, cancel: cancel, running: map[accountingmodel.StateChecksum]struct{}{}, stopped: make(chan struct{})}, nil
}
func (runner *Coordinator) Reconcile(ctx context.Context, request Request) (Evidence, error) {
	validated, validationErr := NewBrokerPositionObservation(ObservationSpec{Provider: request.Observation.Provider, Binding: request.Observation.Binding, InstrumentID: request.Observation.InstrumentID, NetQuantity: request.Observation.NetQuantity, AveragePrice: request.Observation.AveragePrice, AveragePriceReliable: request.Observation.AveragePriceReliable, BrokerObservedAt: request.Observation.BrokerObservedAt, IngestedAt: request.Observation.IngestedAt, SnapshotID: request.Observation.SnapshotID, SnapshotVersion: request.Observation.SnapshotVersion, Complete: request.Observation.Complete, Available: request.Observation.Available, Scope: request.Observation.Scope, SourceChecksum: request.Observation.SourceChecksum})
	if validationErr != nil || request.Observation.IsZero() || validated.ID != request.Observation.ID || !bytes.Equal(validated.CanonicalJSON(), request.Observation.CanonicalJSON()) || request.EvaluatedAt.IsZero() ||
		(request.Mode != ModeOffline && request.Mode != ModePaper && request.Mode != ModeShadow && request.Mode != ModeLiveDisabled) {
		return Evidence{}, ErrInvalidRequest
	}
	resolvedBinding, bindingErr := runner.bindings.Resolve(ctx, request.Observation.Binding.PortfolioID, request.Observation.BrokerObservedAt)
	if bindingErr != nil {
		return Evidence{}, bindingErr
	}
	if resolvedBinding.Checksum() != request.Observation.Binding.Checksum() {
		return Evidence{}, ErrInvalidRequest
	}
	if existing, lookupErr := runner.repository.Evidence(ctx, request.Observation.ID); lookupErr == nil {
		return existing, nil
	} else if !errors.Is(lookupErr, accountingstorage.ErrNotFound) {
		return Evidence{}, lookupErr
	}
	runner.mu.Lock()
	if runner.closed {
		runner.mu.Unlock()
		return Evidence{}, ErrShutdown
	}
	if _, ok := runner.running[request.Observation.ID]; ok {
		runner.mu.Unlock()
		return Evidence{}, ErrDuplicateInProgress
	}
	runner.running[request.Observation.ID] = struct{}{}
	runner.wait.Add(1)
	runner.mu.Unlock()
	defer func() {
		runner.mu.Lock()
		delete(runner.running, request.Observation.ID)
		runner.wait.Done()
		runner.mu.Unlock()
	}()
	select {
	case runner.semaphore <- struct{}{}:
		defer func() { <-runner.semaphore }()
	case <-ctx.Done():
		return Evidence{}, ctx.Err()
	case <-runner.ctx.Done():
		return Evidence{}, ErrShutdown
	}
	opctx, cancel := context.WithTimeout(ctx, runner.config.Timeout)
	defer cancel()
	stop := context.AfterFunc(runner.ctx, cancel)
	defer stop()
	positionID, _ := accountingmodel.NewPositionID(request.Observation.Binding.PortfolioID.String(), request.Observation.InstrumentID.String())
	position, positionErr := runner.positions.Position(opctx, positionID)
	localKnown := positionErr == nil
	if positionErr != nil && !errors.Is(positionErr, accountingstorage.ErrNotFound) {
		return Evidence{}, positionErr
	}
	evidence := buildEvidence(positionID, position, localKnown, request, runner.config)
	if localKnown {
		current, err := runner.positions.Position(opctx, positionID)
		if err != nil || current.Revision() != position.Revision() || current.Checksum() != position.Checksum() {
			return Evidence{}, ErrPositionChanged
		}
	}
	checkpoint, _ := NewCheckpoint(request.Observation.ID, evidence.ID)
	return runner.repository.Publish(opctx, evidence, checkpoint)
}

type StaticAccountBindings map[portfoliomodel.PortfolioID]accountingmodel.AccountBinding

func (bindings StaticAccountBindings) Resolve(ctx context.Context, portfolioID portfoliomodel.PortfolioID, at time.Time) (accountingmodel.AccountBinding, error) {
	if err := ctx.Err(); err != nil {
		return accountingmodel.AccountBinding{}, err
	}
	value, ok := bindings[portfolioID]
	if !ok || value.Validate() != nil || value.ValidFrom.After(at) {
		return accountingmodel.AccountBinding{}, ErrInvalidRequest
	}
	return value, nil
}

func buildEvidence(positionID accountingmodel.PositionID, position accountingmodel.PositionSnapshot, localKnown bool, request Request, config Config) Evidence {
	observation := request.Observation
	local := int64(0)
	revision := accountingmodel.PositionRevision(0)
	positionChecksum := accountingmodel.StateChecksum{}
	if localKnown {
		local = position.Spec().NetQuantity.Int64()
		revision = position.Revision()
		positionChecksum = position.Checksum()
	}
	classification := Match
	severity := SeverityHealthy
	reason := "QUANTITIES_MATCH"
	known := observation.Available && observation.Complete
	fresh := known && !request.EvaluatedAt.Before(observation.BrokerObservedAt) && request.EvaluatedAt.Sub(observation.BrokerObservedAt) <= config.MaximumAge
	if request.Mode == ModeOffline || request.Mode == ModeLiveDisabled || (request.Mode == ModeShadow && observation.Scope == ScopeReal) || (request.Mode == ModePaper && observation.Scope != ScopePaper) {
		classification = Unknown
		severity = SeverityCritical
		reason = "SCOPE_NOT_COMPARABLE"
		known = false
		fresh = false
	} else if !known {
		classification = Unknown
		severity = SeverityCritical
		reason = "BROKER_EVIDENCE_UNAVAILABLE"
	} else if !fresh {
		classification = StaleBrokerObservation
		severity = SeverityCritical
		reason = "BROKER_EVIDENCE_STALE"
	} else if local != 0 && observation.NetQuantity == 0 {
		classification = LocalOnly
		severity = SeverityHigh
		reason = "LOCAL_EXPOSURE_ONLY"
	} else if local == 0 && observation.NetQuantity != 0 {
		classification = BrokerOnly
		severity = SeverityCritical
		reason = "UNMANAGED_BROKER_EXPOSURE"
	} else if signsDiffer(local, observation.NetQuantity) {
		classification = DirectionMismatch
		severity = SeverityHigh
		reason = "POSITION_DIRECTION_DIFFERS"
	} else if local != observation.NetQuantity {
		classification = QuantityMismatch
		severity = SeverityHigh
		reason = "POSITION_QUANTITY_DIFFERS"
	}
	direction := DirectionUnknown
	if known {
		direction = DirectionEqual
		if local != observation.NetQuantity {
			direction = DirectionSame
		}
		if signsDiffer(local, observation.NetQuantity) {
			direction = DirectionOpposite
		}
	}
	raw, _ := json.Marshal(struct {
		ObservationID, PositionID, PositionChecksum, Classification, Severity, Direction, Reason, EvaluatedAt, PolicyVersion string
		PositionRevision                                                                                                     uint64
		LocalQuantity, BrokerQuantity                                                                                        int64
		BrokerQuantityKnown, Fresh, Blocked                                                                                  bool
	}{observation.ID.String(), positionID.String(), positionChecksum.String(), string(classification), string(severity), string(direction), reason, request.EvaluatedAt.UTC().Format(time.RFC3339Nano), config.PolicyVersion, uint64(revision), local, observation.NetQuantity, known, fresh, classification != Match})
	id, _ := accountingmodel.NewStateChecksum("position-reconciliation-evidence/v1", raw)
	return Evidence{ID: id, ObservationID: observation.ID, PositionID: positionID, PositionRevision: revision, PositionChecksum: positionChecksum, Classification: classification, Severity: severity, LocalQuantity: local, BrokerQuantity: observation.NetQuantity, BrokerQuantityKnown: known, Direction: direction, Fresh: fresh, Blocked: classification != Match, Reason: reason, EvaluatedAt: request.EvaluatedAt.UTC(), PolicyVersion: config.PolicyVersion, canonical: raw}
}
func signsDiffer(left, right int64) bool { return left != 0 && right != 0 && (left < 0) != (right < 0) }
func (runner *Coordinator) Shutdown(ctx context.Context) error {
	runner.stopOnce.Do(func() {
		runner.mu.Lock()
		runner.closed = true
		runner.cancel()
		runner.mu.Unlock()
		go func() { runner.wait.Wait(); close(runner.stopped) }()
	})
	select {
	case <-runner.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
