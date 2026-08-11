package zerodha

import (
	"context"
	"errors"
	"sync"
	"time"

	brokertelemetry "github.com/bibhuyash/tradeedge/internal/broker/telemetry"
)

type SessionState string

const (
	SessionUnconfigured         SessionState = "UNCONFIGURED"
	SessionLoginRequired        SessionState = "LOGIN_REQUIRED"
	SessionTokenExchangePending SessionState = "TOKEN_EXCHANGE_PENDING"
	SessionAuthenticated        SessionState = "AUTHENTICATED"
	SessionExpired              SessionState = "EXPIRED"
	SessionAuthFailed           SessionState = "AUTH_FAILED"
	SessionStopped              SessionState = "STOPPED"
)

type SessionSnapshot struct {
	State     SessionState `json:"state"`
	ExpiresAt time.Time    `json:"expires_at,omitempty"`
}

type Clock interface{ Now() time.Time }

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

type TokenExchangeResult struct {
	accessToken string
	expiresAt   time.Time
}

type TokenExchanger interface {
	Exchange(context.Context, string, string, string) (TokenExchangeResult, error)
}

type SessionManager struct {
	mu          sync.RWMutex
	clock       Clock
	credentials CredentialMaterial
	exchanger   TokenExchanger
	telemetry   brokertelemetry.Recorder
	state       SessionState
	accessToken string
	expiresAt   time.Time
}

func NewSessionManager(credentials CredentialMaterial, exchanger TokenExchanger, clock Clock, recorder brokertelemetry.Recorder) *SessionManager {
	if clock == nil {
		clock = RealClock{}
	}
	manager := &SessionManager{clock: clock, credentials: credentials, exchanger: exchanger, telemetry: brokertelemetry.Safe(recorder), state: SessionLoginRequired}
	if credentials.apiKey == "" || credentials.apiSecret == "" {
		manager.state = SessionUnconfigured
	} else if credentials.accessToken != "" {
		manager.accessToken = credentials.accessToken
		manager.expiresAt = credentials.expiresAt
		if !credentials.expiresAt.After(clock.Now()) {
			manager.state = SessionExpired
		} else {
			manager.state = SessionAuthenticated
		}
	}
	return manager
}

func (manager *SessionManager) Authenticate(ctx context.Context) error {
	started := manager.clock.Now()
	manager.mu.Lock()
	if manager.state == SessionStopped {
		manager.mu.Unlock()
		return ErrStopped
	}
	if manager.state == SessionAuthenticated && manager.expiresAt.After(manager.clock.Now()) {
		manager.mu.Unlock()
		return nil
	}
	if manager.credentials.requestToken == "" || manager.exchanger == nil {
		manager.state = SessionLoginRequired
		manager.mu.Unlock()
		return ErrCredentialsMissing
	}
	manager.state = SessionTokenExchangePending
	apiKey, apiSecret, requestToken := manager.credentials.apiKey, manager.credentials.apiSecret, manager.credentials.requestToken
	manager.mu.Unlock()

	result, err := manager.exchanger.Exchange(ctx, apiKey, apiSecret, requestToken)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.credentials.requestToken = ""
	if err != nil || result.accessToken == "" || !result.expiresAt.After(manager.clock.Now()) {
		manager.state = SessionAuthFailed
		manager.telemetry.Record(brokertelemetry.Event{Operation: brokertelemetry.OperationAuthentication, Outcome: brokertelemetry.OutcomeFailure, Duration: manager.clock.Now().Sub(started)})
		var providerFailure AuthenticationFailure
		if errors.As(err, &providerFailure) {
			return errors.Join(ErrAuthentication, providerFailure)
		}
		return ErrAuthentication
	}
	manager.accessToken = result.accessToken
	manager.expiresAt = result.expiresAt.UTC()
	manager.state = SessionAuthenticated
	manager.telemetry.Record(brokertelemetry.Event{Operation: brokertelemetry.OperationAuthentication, Outcome: brokertelemetry.OutcomeSuccess, Duration: manager.clock.Now().Sub(started)})
	return nil
}

func (manager *SessionManager) Authorization() (string, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.state == SessionStopped {
		return "", ErrStopped
	}
	if manager.state != SessionAuthenticated {
		if manager.state == SessionExpired {
			return "", ErrSessionExpired
		}
		return "", ErrAuthentication
	}
	if !manager.expiresAt.After(manager.clock.Now()) {
		manager.state = SessionExpired
		manager.accessToken = ""
		manager.telemetry.Record(brokertelemetry.Event{Operation: brokertelemetry.OperationAuthentication, Outcome: brokertelemetry.OutcomeExpired})
		return "", ErrSessionExpired
	}
	return "token " + manager.credentials.apiKey + ":" + manager.accessToken, nil
}

func (manager *SessionManager) Snapshot() SessionSnapshot {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	state := manager.state
	if state == SessionAuthenticated && !manager.expiresAt.After(manager.clock.Now()) {
		state = SessionExpired
	}
	return SessionSnapshot{State: state, ExpiresAt: manager.expiresAt}
}

func (manager *SessionManager) Expire() {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.accessToken = ""
	manager.state = SessionExpired
}

func (manager *SessionManager) Shutdown() {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.accessToken = ""
	manager.credentials = CredentialMaterial{}
	manager.state = SessionStopped
}
