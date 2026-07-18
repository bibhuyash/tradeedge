package model

import "errors"

var ErrInvalidLifecycleState = errors.New("invalid strategy lifecycle state")

type LifecycleState string

const (
	LifecycleCandidate LifecycleState = "CANDIDATE"
	LifecycleProbation LifecycleState = "PROBATION"
	LifecycleActive    LifecycleState = "ACTIVE"
	LifecycleSuspended LifecycleState = "SUSPENDED"
	LifecycleRetired   LifecycleState = "RETIRED"
)

func (state LifecycleState) Validate() error {
	switch state {
	case LifecycleCandidate, LifecycleProbation, LifecycleActive,
		LifecycleSuspended, LifecycleRetired:
		return nil
	default:
		return ErrInvalidLifecycleState
	}
}

func (state LifecycleState) Evaluates() bool {
	return state == LifecycleCandidate || state == LifecycleProbation || state == LifecycleActive
}
