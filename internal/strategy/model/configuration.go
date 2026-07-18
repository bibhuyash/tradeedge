package model

import (
	"crypto/sha256"
	"errors"
	"strings"
)

const MaximumConfigurationBytes = 64 << 10

var ErrInvalidConfiguration = errors.New("invalid strategy configuration")

type StrategyConfiguration struct {
	schemaVersion string
	canonicalJSON []byte
	hash          ConfigurationHash
}

func NewStrategyConfiguration(schemaVersion string, raw []byte) (StrategyConfiguration, error) {
	schemaVersion = strings.TrimSpace(schemaVersion)
	if schemaVersion == "" {
		return StrategyConfiguration{}, ErrInvalidConfiguration
	}
	canonical, err := canonicalJSONObject(raw, MaximumConfigurationBytes)
	if err != nil {
		return StrategyConfiguration{}, errors.Join(ErrInvalidConfiguration, err)
	}
	digest := sha256.Sum256([]byte(stableKey(
		"strategy-configuration/v1", schemaVersion, string(canonical),
	)))
	return StrategyConfiguration{
		schemaVersion: schemaVersion,
		canonicalJSON: canonical,
		hash:          ConfigurationHash(digest),
	}, nil
}

func (configuration StrategyConfiguration) SchemaVersion() string {
	return configuration.schemaVersion
}

func (configuration StrategyConfiguration) CanonicalJSON() []byte {
	return append([]byte(nil), configuration.canonicalJSON...)
}

func (configuration StrategyConfiguration) Hash() ConfigurationHash { return configuration.hash }
func (configuration StrategyConfiguration) IsZero() bool {
	return configuration.schemaVersion == "" || configuration.hash.IsZero()
}
