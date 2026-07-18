package model

import (
	"crypto/sha256"
	"errors"
	"strings"
)

const MaximumStateBytes = 64 << 10

var ErrInvalidRuntimeState = errors.New("invalid strategy runtime state")

type StrategyRuntimeState struct {
	schemaVersion string
	canonicalJSON []byte
	hash          StateHash
}

func NewStrategyRuntimeState(schemaVersion string, raw []byte) (StrategyRuntimeState, error) {
	schemaVersion = strings.TrimSpace(schemaVersion)
	if schemaVersion == "" {
		return StrategyRuntimeState{}, ErrInvalidRuntimeState
	}
	canonical, err := canonicalJSONObject(raw, MaximumStateBytes)
	if err != nil {
		return StrategyRuntimeState{}, errors.Join(ErrInvalidRuntimeState, err)
	}
	digest := sha256.Sum256([]byte(stableKey(
		"strategy-runtime-state/v1", schemaVersion, string(canonical),
	)))
	return StrategyRuntimeState{
		schemaVersion: schemaVersion,
		canonicalJSON: canonical,
		hash:          StateHash(digest),
	}, nil
}

func (state StrategyRuntimeState) SchemaVersion() string { return state.schemaVersion }
func (state StrategyRuntimeState) CanonicalJSON() []byte {
	return append([]byte(nil), state.canonicalJSON...)
}
func (state StrategyRuntimeState) Hash() StateHash { return state.hash }
func (state StrategyRuntimeState) Size() int       { return len(state.canonicalJSON) }
func (state StrategyRuntimeState) IsZero() bool {
	return state.schemaVersion == "" || state.hash.IsZero()
}
