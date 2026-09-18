package session

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const ProviderZerodha = "zerodha"

type State string

const (
	StateLoginRequired State = "LOGIN_REQUIRED"
	StateAuthenticated State = "AUTHENTICATED"
	StateExpired       State = "EXPIRED"
	StateError         State = "ERROR"
)

var (
	ErrNotFound            = errors.New("session not found")
	ErrCorrupt             = errors.New("session store corrupt")
	ErrInvalidRequestToken = errors.New("invalid request token")
	ErrUnavailable         = errors.New("authentication provider unavailable")
	ErrBusy                = errors.New("authentication exchange already in progress")
	ErrInvalid             = errors.New("invalid session configuration")
)

type Status struct {
	Provider  string    `json:"provider"`
	State     State     `json:"state"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Reused    bool      `json:"reused"`
}

func (s Status) MarshalJSON() ([]byte, error) {
	type statusJSON struct {
		Provider  string     `json:"provider"`
		State     State      `json:"state"`
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
		Reused    bool       `json:"reused"`
	}
	value := statusJSON{Provider: s.Provider, State: s.State, Reused: s.Reused}
	if !s.ExpiresAt.IsZero() {
		expiresAt := s.ExpiresAt.UTC()
		value.ExpiresAt = &expiresAt
	}
	return json.Marshal(value)
}

type Record struct {
	Provider        string
	AccessToken     string
	ExpiresAt       time.Time
	AuthenticatedAt time.Time
}

func (Record) String() string                 { return "[REDACTED]" }
func (Record) Format(state fmt.State, _ rune) { _, _ = state.Write([]byte("[REDACTED]")) }
func (Record) LogValue() slog.Value           { return slog.StringValue("[REDACTED]") }

type Store interface {
	Load(context.Context) (Record, error)
	Save(context.Context, Record) error
	Delete(context.Context) error
}

type AuthPort interface {
	LoginURL(context.Context) (string, error)
	Exchange(context.Context, string) (Record, error)
}

type Clock interface{ Now() time.Time }

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

type attempt struct {
	key    [sha256.Size]byte
	done   chan struct{}
	status Status
	err    error
}

type Service struct {
	mu       sync.Mutex
	store    Store
	auth     AuthPort
	clock    Clock
	status   Status
	record   Record
	blocked  bool
	inflight *attempt
}

func NewService(ctx context.Context, store Store, auth AuthPort, clock Clock) (*Service, error) {
	if store == nil || auth == nil {
		return nil, ErrInvalid
	}
	if clock == nil {
		clock = RealClock{}
	}
	service := &Service{
		store:  store,
		auth:   auth,
		clock:  clock,
		status: Status{Provider: ProviderZerodha, State: StateLoginRequired},
	}
	record, err := store.Load(ctx)
	switch {
	case err == nil:
		if !validRecord(record) {
			service.status.State = StateError
			service.blocked = true
			break
		}
		service.record = record
		service.status.ExpiresAt = record.ExpiresAt.UTC()
		if record.ExpiresAt.After(clock.Now()) {
			service.status.State = StateAuthenticated
			service.status.Reused = true
		} else {
			service.status.State = StateExpired
		}
	case errors.Is(err, ErrNotFound):
	case err != nil:
		service.status.State = StateError
		service.blocked = true
	}
	return service, nil
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshExpiryLocked()
	return s.status, nil
}

func (s *Service) LoginURL(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return s.auth.LoginURL(ctx)
}

func (s *Service) Exchange(ctx context.Context, requestToken string) (Status, error) {
	requestToken = strings.TrimSpace(requestToken)
	if requestToken == "" || len(requestToken) > 2048 || strings.ContainsAny(requestToken, "\r\n\x00") {
		return Status{Provider: ProviderZerodha, State: StateLoginRequired}, ErrInvalidRequestToken
	}
	key := sha256.Sum256([]byte(requestToken))

	s.mu.Lock()
	s.refreshExpiryLocked()
	if s.blocked {
		status := s.status
		s.mu.Unlock()
		return status, ErrCorrupt
	}
	if s.status.State == StateAuthenticated {
		status := s.status
		s.mu.Unlock()
		return status, nil
	}
	if current := s.inflight; current != nil {
		if current.key != key {
			status := s.status
			s.mu.Unlock()
			return status, ErrBusy
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return Status{}, ctx.Err()
		case <-current.done:
			return current.status, current.err
		}
	}
	current := &attempt{key: key, done: make(chan struct{})}
	s.inflight = current
	s.mu.Unlock()

	record, exchangeErr := s.auth.Exchange(ctx, requestToken)
	requestToken = ""

	s.mu.Lock()
	defer s.mu.Unlock()
	exchangeErr = s.applyExchangeLocked(ctx, record, exchangeErr)
	current.status, current.err = s.status, exchangeErr
	s.inflight = nil
	close(current.done)
	return current.status, current.err
}

func (s *Service) applyExchangeLocked(ctx context.Context, record Record, exchangeErr error) error {
	if exchangeErr != nil {
		s.record = Record{}
		s.status.ExpiresAt = time.Time{}
		s.status.Reused = false
		if errors.Is(exchangeErr, ErrInvalidRequestToken) {
			s.status.State = StateLoginRequired
		} else {
			s.status.State = StateError
		}
		return exchangeErr
	}
	if !validRecord(record) || !record.ExpiresAt.After(s.clock.Now()) {
		s.record = Record{}
		s.status = Status{Provider: ProviderZerodha, State: StateError}
		return ErrUnavailable
	}
	if err := s.store.Save(ctx, record); err != nil {
		s.record = Record{}
		s.status = Status{Provider: ProviderZerodha, State: StateError}
		return ErrUnavailable
	}
	s.record = record
	s.status = Status{Provider: ProviderZerodha, State: StateAuthenticated, ExpiresAt: record.ExpiresAt.UTC(), Reused: false}
	return nil
}

func (s *Service) refreshExpiryLocked() {
	if s.status.State == StateAuthenticated && !s.status.ExpiresAt.After(s.clock.Now()) {
		s.status.State = StateExpired
		s.status.Reused = false
		s.record = Record{}
	}
}

func validRecord(record Record) bool {
	return record.Provider == ProviderZerodha && strings.TrimSpace(record.AccessToken) != "" &&
		!record.ExpiresAt.IsZero() && !record.AuthenticatedAt.IsZero() &&
		!record.AuthenticatedAt.After(record.ExpiresAt) &&
		!strings.ContainsAny(record.AccessToken, "\r\n\x00")
}
