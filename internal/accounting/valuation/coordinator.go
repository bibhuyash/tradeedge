package valuation

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
)

var (
	ErrDuplicateInProgress = errors.New("portfolio valuation already in progress")
	ErrSourceChanged       = errors.New("valuation source changed during calculation")
	ErrShutdown            = errors.New("valuation coordinator shut down")
	ErrPanic               = errors.New("valuation calculation panicked")
)

type PositionSource interface {
	Positions(context.Context, portfoliomodel.PortfolioID) ([]accountingmodel.PositionSnapshot, accountingmodel.StateChecksum, error)
}
type MarkSource interface {
	Marks(context.Context, []accountingmodel.PositionSnapshot) (map[accountingmodel.PositionID]MarkPrice, accountingmodel.StateChecksum, error)
}
type Config struct {
	MaxConcurrency int
	Timeout        time.Duration
	Policy         Policy
}

func DefaultConfig() Config { return Config{4, time.Second, DefaultPolicy()} }

type Health struct {
	Closed      bool   `json:"closed"`
	InFlight    int    `json:"in_flight"`
	Keyed       int    `json:"keyed_portfolios"`
	Maximum     int    `json:"maximum_concurrency"`
	LastFailure Reason `json:"last_failure,omitempty"`
}

type Coordinator struct {
	positions   PositionSource
	marks       MarkSource
	repository  Repository
	telemetry   Recorder
	config      Config
	semaphore   chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	running     map[portfoliomodel.PortfolioID]struct{}
	closed      bool
	inFlight    int
	lastFailure Reason
	wait        sync.WaitGroup
	stopOnce    sync.Once
	stopped     chan struct{}
}

func NewCoordinator(positions PositionSource, marks MarkSource, repository Repository, telemetry Recorder, config Config) (*Coordinator, error) {
	if positions == nil || marks == nil || repository == nil || config.MaxConcurrency <= 0 || config.MaxConcurrency > 64 || config.Timeout <= 0 || config.Timeout > time.Minute || config.Policy.Validate() != nil {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Coordinator{positions: positions, marks: marks, repository: repository, telemetry: SafeRecorder(telemetry), config: config, semaphore: make(chan struct{}, config.MaxConcurrency), ctx: ctx, cancel: cancel, running: map[portfoliomodel.PortfolioID]struct{}{}, stopped: make(chan struct{})}, nil
}
func (c *Coordinator) Health() Health {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Health{c.closed, c.inFlight, len(c.running), c.config.MaxConcurrency, c.lastFailure}
}

func (c *Coordinator) Value(ctx context.Context, portfolioID portfoliomodel.PortfolioID, at time.Time) (receipt Receipt, err error) {
	started := time.Now()
	outcome := OutcomeFailed
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		c.telemetry.Record(Event{Operation: OperationValuation, Outcome: outcome, Status: statusFromError(err), Duration: time.Since(started), InFlight: c.Health().InFlight})
	}()
	if portfolioID.IsZero() || at.IsZero() {
		return receipt, ErrInvalid
	}
	if err = c.reserve(portfolioID); err != nil {
		return receipt, err
	}
	defer c.release(portfolioID)
	select {
	case c.semaphore <- struct{}{}:
		defer func() { <-c.semaphore }()
	case <-ctx.Done():
		return receipt, ctx.Err()
	case <-c.ctx.Done():
		return receipt, ErrShutdown
	}
	opCtx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	positions, positionManifest, err := c.positions.Positions(opCtx, portfolioID)
	if err != nil {
		return receipt, err
	}
	if len(positions) == 0 || len(positions) > MaximumPositionsPerPortfolio {
		return receipt, ErrInvalid
	}
	sort.Slice(positions, func(i, j int) bool { return positions[i].ID().String() < positions[j].ID().String() })
	marks, markManifest, err := c.marks.Marks(opCtx, positions)
	if err != nil {
		return receipt, err
	}
	valuations := make([]PositionValuation, 0, len(positions))
	for _, position := range positions {
		var mark *MarkPrice
		if value, ok := marks[position.ID()]; ok {
			copy := value
			mark = &copy
		}
		valuation, valueErr := EvaluatePosition(position, mark, at, c.config.Policy)
		if valueErr != nil {
			return receipt, valueErr
		}
		valuations = append(valuations, valuation)
	}
	current, checkpoint, currentErr := c.repository.Current(opCtx, portfolioID)
	revision := uint64(1)
	if currentErr == nil {
		if current.GeneratedAt.Equal(at.UTC()) && sameSources(current.Sources, valuations) {
			outcome = OutcomeDuplicate
			return Receipt{Revision: current.Revision, SnapshotID: current.ID, CheckpointChecksum: checkpoint, Idempotent: true}, nil
		}
		revision = current.Revision + 1
	} else if !errors.Is(currentErr, ErrNotFound) {
		return receipt, currentErr
	}
	snapshot, err := Aggregate(portfolioID, revision, valuations, at)
	if err != nil {
		return receipt, err
	}
	_, verifyPositions, err := c.positions.Positions(opCtx, portfolioID)
	if err != nil || verifyPositions != positionManifest {
		return receipt, ErrSourceChanged
	}
	_, verifyMarks, err := c.marks.Marks(opCtx, positions)
	if err != nil || verifyMarks != markManifest {
		return receipt, ErrSourceChanged
	}
	raw, _ := json.Marshal(struct{ Parent, Snapshot, Positions, Marks string }{checkpoint.String(), snapshot.Checksum.String(), positionManifest.String(), markManifest.String()})
	checkpointSum, _ := accountingmodel.NewStateChecksum("portfolio-financial-checkpoint/v1", raw)
	publication := Publication{ExpectedRevision: revision - 1, ExpectedCheckpoint: checkpoint, PositionManifest: positionManifest, MarkManifest: markManifest, Valuations: valuations, Snapshot: snapshot, CheckpointChecksum: checkpointSum}
	receipt, err = c.repository.Publish(opCtx, publication)
	if err == nil {
		outcome = OutcomeCompleted
	}
	return receipt, err
}

func sameSources(sources []SourceRevision, valuations []PositionValuation) bool {
	if len(sources) != len(valuations) {
		return false
	}
	values := append([]PositionValuation(nil), valuations...)
	sort.Slice(values, func(i, j int) bool { return values[i].PositionID.String() < values[j].PositionID.String() })
	for index, source := range sources {
		value := values[index]
		market := accountingmodel.StateChecksum{}
		if value.Mark != nil {
			market = value.Mark.MarketChecksum
		}
		if source.PositionID != value.PositionID || source.PositionRevision != value.PositionRevision || source.PositionChecksum != value.PositionChecksum || source.ValuationChecksum != value.Checksum || source.MarketChecksum != market {
			return false
		}
	}
	return true
}
func (c *Coordinator) reserve(id portfoliomodel.PortfolioID) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrShutdown
	}
	if _, ok := c.running[id]; ok {
		return ErrDuplicateInProgress
	}
	c.running[id] = struct{}{}
	c.inFlight++
	c.wait.Add(1)
	return nil
}
func (c *Coordinator) release(id portfoliomodel.PortfolioID) {
	c.mu.Lock()
	delete(c.running, id)
	c.inFlight--
	c.wait.Done()
	c.mu.Unlock()
}
func (c *Coordinator) Shutdown(ctx context.Context) error {
	c.stopOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.cancel()
		c.mu.Unlock()
		go func() { c.wait.Wait(); close(c.stopped) }()
	})
	select {
	case <-c.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func statusFromError(err error) Status {
	if err == nil {
		return StatusComplete
	}
	return StatusUnavailable
}
