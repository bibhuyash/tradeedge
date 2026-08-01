package model

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

var ErrInvalidControlState = errors.New("invalid portfolio control state")

type ControlReason string

var controlReasonPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,127}$`)

func NewControlReason(value string) (ControlReason, error) {
	value = strings.TrimSpace(value)
	if !controlReasonPattern.MatchString(value) {
		return "", ErrInvalidControlState
	}
	return ControlReason(value), nil
}

type ControlScope string

const (
	ScopeGlobal             ControlScope = "GLOBAL"
	ScopePortfolio          ControlScope = "PORTFOLIO"
	ScopeStrategyDefinition ControlScope = "STRATEGY_DEFINITION"
	ScopeStrategyInstance   ControlScope = "STRATEGY_INSTANCE"
	ScopeInstrument         ControlScope = "INSTRUMENT"
	ScopeUnderlying         ControlScope = "UNDERLYING"
	ScopeExposureGroup      ControlScope = "EXPOSURE_GROUP"
)

func (value ControlScope) Validate() error {
	switch value {
	case ScopeGlobal, ScopePortfolio, ScopeStrategyDefinition, ScopeStrategyInstance,
		ScopeInstrument, ScopeUnderlying, ScopeExposureGroup:
		return nil
	default:
		return ErrInvalidControlState
	}
}

type KillSwitchState string

const (
	KillSwitchInactive                KillSwitchState = "INACTIVE"
	KillSwitchActive                  KillSwitchState = "ACTIVE"
	KillSwitchRecoveryPending         KillSwitchState = "RECOVERY_PENDING"
	KillSwitchDisabledByConfiguration KillSwitchState = "DISABLED_BY_CONFIGURATION"
)

type KillSwitchSpec struct {
	ID                 KillSwitchID
	Scope              ControlScope
	ScopeSubject       string
	State              KillSwitchState
	ReasonCode         ControlReason
	ActivationEvidence StateChecksum
	ActivatedAt        time.Time
	ExpiresAt          time.Time
	ConfigurationID    PortfolioConfigurationID
	ConfigurationHash  ConfigurationHash
	StateRevision      uint64
	SchemaVersion      string
}

type KillSwitch struct{ spec KillSwitchSpec }

func NewKillSwitch(spec KillSwitchSpec) (KillSwitch, error) {
	spec.ScopeSubject = strings.TrimSpace(spec.ScopeSubject)
	reason, reasonErr := NewControlReason(string(spec.ReasonCode))
	spec.ReasonCode = reason
	if spec.ID.IsZero() || spec.Scope.Validate() != nil || spec.ScopeSubject == "" ||
		len(spec.ScopeSubject) > MaximumSubjectBytes || reasonErr != nil ||
		spec.ConfigurationID.IsZero() ||
		spec.ConfigurationHash.IsZero() || spec.StateRevision == 0 ||
		strings.TrimSpace(spec.SchemaVersion) == "" {
		return KillSwitch{}, ErrInvalidControlState
	}
	switch spec.State {
	case KillSwitchInactive, KillSwitchDisabledByConfiguration:
		if !spec.ActivatedAt.IsZero() || !spec.ActivationEvidence.IsZero() || !spec.ExpiresAt.IsZero() {
			return KillSwitch{}, ErrInvalidControlState
		}
	case KillSwitchActive, KillSwitchRecoveryPending:
		if spec.ActivatedAt.IsZero() || spec.ActivationEvidence.IsZero() ||
			(!spec.ExpiresAt.IsZero() && !spec.ExpiresAt.After(spec.ActivatedAt)) {
			return KillSwitch{}, ErrInvalidControlState
		}
	default:
		return KillSwitch{}, ErrInvalidControlState
	}
	spec.ActivatedAt = spec.ActivatedAt.UTC()
	spec.ExpiresAt = spec.ExpiresAt.UTC()
	return KillSwitch{spec: spec}, nil
}

func (value KillSwitch) Spec() KillSwitchSpec { return value.spec }
func (value KillSwitch) Blocks() bool {
	return value.spec.State == KillSwitchActive || value.spec.State == KillSwitchRecoveryPending
}

type CircuitBreakerState string

const (
	CircuitBreakerClosed   CircuitBreakerState = "CLOSED"
	CircuitBreakerOpen     CircuitBreakerState = "OPEN"
	CircuitBreakerHalfOpen CircuitBreakerState = "HALF_OPEN"
	CircuitBreakerDisabled CircuitBreakerState = "DISABLED"
)

type CircuitBreakerSpec struct {
	ID                CircuitBreakerID
	Scope             ControlScope
	ScopeSubject      string
	State             CircuitBreakerState
	ReasonCode        ControlReason
	Evidence          StateChecksum
	ChangedAt         time.Time
	ConfigurationID   PortfolioConfigurationID
	ConfigurationHash ConfigurationHash
	StateRevision     uint64
	SchemaVersion     string
}

type CircuitBreaker struct{ spec CircuitBreakerSpec }

func NewCircuitBreaker(spec CircuitBreakerSpec) (CircuitBreaker, error) {
	spec.ScopeSubject = strings.TrimSpace(spec.ScopeSubject)
	reason, reasonErr := NewControlReason(string(spec.ReasonCode))
	spec.ReasonCode = reason
	if spec.ID.IsZero() || spec.Scope.Validate() != nil || spec.ScopeSubject == "" ||
		len(spec.ScopeSubject) > MaximumSubjectBytes || reasonErr != nil ||
		spec.ConfigurationID.IsZero() ||
		spec.ConfigurationHash.IsZero() || spec.StateRevision == 0 ||
		strings.TrimSpace(spec.SchemaVersion) == "" {
		return CircuitBreaker{}, ErrInvalidControlState
	}
	switch spec.State {
	case CircuitBreakerClosed, CircuitBreakerDisabled:
	case CircuitBreakerOpen, CircuitBreakerHalfOpen:
		if spec.ChangedAt.IsZero() || spec.Evidence.IsZero() {
			return CircuitBreaker{}, ErrInvalidControlState
		}
	default:
		return CircuitBreaker{}, ErrInvalidControlState
	}
	spec.ChangedAt = spec.ChangedAt.UTC()
	return CircuitBreaker{spec: spec}, nil
}

func (value CircuitBreaker) Spec() CircuitBreakerSpec { return value.spec }
func (value CircuitBreaker) Blocks() bool {
	return value.spec.State == CircuitBreakerOpen || value.spec.State == CircuitBreakerHalfOpen
}
