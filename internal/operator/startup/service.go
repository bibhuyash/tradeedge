// Package startup owns the operator-facing SHADOW startup state machine.
// It contains no Docker or broker implementation details.
package startup

import (
	"context"
	"errors"
	"sync"
)

type State string

const (
	LoginRequired       State = "LOGIN_REQUIRED"
	Authenticated       State = "AUTHENTICATED"
	Preparing           State = "PREPARING"
	StartingShadow      State = "STARTING_SHADOW"
	WaitingForReadiness State = "WAITING_FOR_READINESS"
	Ready               State = "READY"
	MarketClosed        State = "MARKET_CLOSED"
	Failed              State = "FAILED"
)

type StepState string

const (
	StepPass    StepState = "PASS"
	StepRunning StepState = "RUNNING"
	StepFailed  StepState = "FAILED"
)

type Step struct {
	Name  string    `json:"name"`
	State StepState `json:"state"`
	Error string    `json:"error,omitempty"`
}
type Status struct {
	State       State  `json:"state"`
	CurrentStep string `json:"current_step,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Steps       []Step `json:"steps"`
}

type PreparationService interface{ Prepare(context.Context) error }
type RuntimeManager interface {
	Status(context.Context) (running bool, healthy bool, err error)
	StartShadow(context.Context) error
}
type ReadinessClient interface {
	Ready(context.Context) (bool, error)
}
type SessionSource interface {
	Authenticated(context.Context) (bool, error)
}
type MarketSource interface {
	Closed(context.Context) (closed bool, reason string, err error)
}

type Service struct {
	preparation PreparationService
	runtime     RuntimeManager
	readiness   ReadinessClient
	session     SessionSource
	market      MarketSource
	mu          sync.Mutex
	status      Status
	running     bool
	done        chan struct{}
}

func New(preparation PreparationService, runtime RuntimeManager, readiness ReadinessClient, session SessionSource, market MarketSource) (*Service, error) {
	if preparation == nil || runtime == nil || readiness == nil || session == nil || market == nil {
		return nil, errors.New("startup dependencies are required")
	}
	return &Service{preparation: preparation, runtime: runtime, readiness: readiness, session: session, market: market, status: Status{State: LoginRequired}}, nil
}

func (s *Service) Status(context.Context) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.status)
}

func (s *Service) Start(ctx context.Context) Status {
	s.mu.Lock()
	if s.status.State == Ready {
		status := clone(s.status)
		s.mu.Unlock()
		return status
	}
	if s.running {
		status, done := clone(s.status), s.done
		s.mu.Unlock()
		select {
		case <-done:
			return s.Status(ctx)
		case <-ctx.Done():
			return status
		}
	}
	s.running, s.done = true, make(chan struct{})
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.running = false; close(s.done); s.mu.Unlock() }()
	return s.run(ctx)
}

func (s *Service) run(ctx context.Context) Status {
	authenticated, err := s.session.Authenticated(ctx)
	if err != nil {
		return s.fail("SESSION", err)
	}
	if !authenticated {
		return s.set(Status{State: LoginRequired})
	}
	closed, reason, err := s.market.Closed(ctx)
	if err != nil {
		return s.fail("MARKET_SESSION", err)
	}
	if closed {
		return s.set(Status{State: MarketClosed, Reason: reason, Steps: []Step{{Name: "SESSION", State: StepPass}, {Name: "MARKET_SESSION", State: StepPass}}})
	}
	running, healthy, err := s.runtime.Status(ctx)
	if err != nil {
		return s.fail("RUNTIME", err)
	}
	if running && healthy {
		ready, readyErr := s.readiness.Ready(ctx)
		if readyErr != nil || !ready {
			if readyErr == nil {
				readyErr = errors.New("shadow readiness timeout")
			}
			return s.fail("READINESS", readyErr)
		}
		return s.set(Status{State: Ready, Steps: []Step{{Name: "SESSION", State: StepPass}, {Name: "START_SHADOW", State: StepPass}, {Name: "READINESS", State: StepPass}}})
	}
	s.set(Status{State: Preparing, CurrentStep: "PREPARATION", Steps: []Step{{Name: "SESSION", State: StepPass}, {Name: "PREPARATION", State: StepRunning}}})
	if err := s.preparation.Prepare(ctx); err != nil {
		return s.fail("PREPARATION", err)
	}
	running, healthy, err = s.runtime.Status(ctx)
	if err != nil {
		return s.fail("RUNTIME", err)
	}
	if !running || !healthy {
		s.set(Status{State: StartingShadow, CurrentStep: "START_SHADOW", Steps: []Step{{Name: "SESSION", State: StepPass}, {Name: "PREPARATION", State: StepPass}, {Name: "START_SHADOW", State: StepRunning}}})
		if err := s.runtime.StartShadow(ctx); err != nil {
			return s.fail("START_SHADOW", err)
		}
	}
	s.set(Status{State: WaitingForReadiness, CurrentStep: "READINESS", Steps: []Step{{Name: "SESSION", State: StepPass}, {Name: "PREPARATION", State: StepPass}, {Name: "START_SHADOW", State: StepPass}, {Name: "READINESS", State: StepRunning}}})
	ready, err := s.readiness.Ready(ctx)
	if err != nil || !ready {
		if err == nil {
			err = errors.New("shadow readiness timeout")
		}
		return s.fail("READINESS", err)
	}
	return s.set(Status{State: Ready, Steps: []Step{{Name: "SESSION", State: StepPass}, {Name: "PREPARATION", State: StepPass}, {Name: "START_SHADOW", State: StepPass}, {Name: "READINESS", State: StepPass}}})
}

func (s *Service) set(status Status) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = clone(status)
	return clone(status)
}
func (s *Service) fail(step string, err error) Status {
	return s.set(Status{State: Failed, CurrentStep: step, Reason: err.Error(), Steps: []Step{{Name: step, State: StepFailed, Error: err.Error()}}})
}
func clone(status Status) Status { status.Steps = append([]Step(nil), status.Steps...); return status }
