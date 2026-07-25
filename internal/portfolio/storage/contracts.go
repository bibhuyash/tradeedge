package storage

import (
	"context"
	"errors"
	"fmt"

	portfolioconfig "github.com/bibhuyash/tradeedge/internal/portfolio/config"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
)

var (
	ErrNotFound          = errors.New("portfolio repository record not found")
	ErrIdentityCollision = errors.New("portfolio repository identity collision")
	ErrCapacityExhausted = errors.New("portfolio repository capacity exhausted")
	ErrInternal          = errors.New("portfolio repository internal failure")
)

type IdentityCollisionError struct {
	Kind     string
	Identity string
}

func (value *IdentityCollisionError) Error() string {
	return fmt.Sprintf("%v: %s %s", ErrIdentityCollision, value.Kind, value.Identity)
}
func (value *IdentityCollisionError) Unwrap() error { return ErrIdentityCollision }

type RegistrationStatus string

const (
	RegistrationCommitted  RegistrationStatus = "COMMITTED"
	RegistrationIdempotent RegistrationStatus = "IDEMPOTENT_REPLAY"
)

type RegistrationOutcome struct{ Status RegistrationStatus }

type PortfolioConfigurationRepository interface {
	RegisterConfiguration(context.Context, portfolioconfig.PortfolioConfiguration) (RegistrationOutcome, error)
	Configuration(context.Context, portfoliomodel.PortfolioConfigurationID) (portfolioconfig.PortfolioConfiguration, error)
	Configurations(context.Context) ([]portfolioconfig.PortfolioConfiguration, error)
}

type AllocationPolicyRepository interface {
	AllocationPolicy(context.Context, portfoliomodel.AllocationPolicyID) (portfolioconfig.AllocationPolicy, error)
}

type PortfolioSnapshotRepository interface {
	RegisterSnapshot(context.Context, portfoliomodel.PortfolioSnapshot) (RegistrationOutcome, error)
	Snapshot(context.Context, portfoliomodel.PortfolioSnapshotID) (portfoliomodel.PortfolioSnapshot, error)
	Snapshots(context.Context, portfoliomodel.PortfolioID) ([]portfoliomodel.PortfolioSnapshot, error)
}
