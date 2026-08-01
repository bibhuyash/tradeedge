package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	portfolioconfig "github.com/bibhuyash/tradeedge/internal/portfolio/config"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	portfoliostorage "github.com/bibhuyash/tradeedge/internal/portfolio/storage"
)

func TestConfigurationRepositoryIdempotencyOrderingAndCancellation(t *testing.T) {
	store := NewStoreWithLimits(Limits{Configurations: 2, Snapshots: 1})
	first := decode(t, configurationOne)
	second := decode(t, configurationTwo)
	outcome, err := store.RegisterConfiguration(context.Background(), second)
	if err != nil || outcome.Status != portfoliostorage.RegistrationCommitted {
		t.Fatalf("register = %#v, %v", outcome, err)
	}
	outcome, err = store.RegisterConfiguration(context.Background(), second)
	if err != nil || outcome.Status != portfoliostorage.RegistrationIdempotent {
		t.Fatalf("idempotent = %#v, %v", outcome, err)
	}
	if _, err := store.RegisterConfiguration(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	values, err := store.Configurations(context.Background())
	if err != nil || len(values) != 2 || values[0].ID().String() > values[1].ID().String() {
		t.Fatalf("values = %#v, %v", values, err)
	}
	if _, err := store.Configuration(context.Background(),
		portfoliomodel.PortfolioConfigurationID{}); !errors.Is(err, portfoliostorage.ErrNotFound) {
		t.Fatalf("not found error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.RegisterConfiguration(ctx, first); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	groups := values[0].AllocationPolicy().Limits.ExposureGroups
	groups[0] = "MUTATED"
	stored, _ := store.Configuration(context.Background(), values[0].ID())
	if stored.AllocationPolicy().Limits.ExposureGroups[0] == "MUTATED" {
		t.Fatal("repository value was mutated")
	}
}

func TestSnapshotRepositoryIdempotencyAndDefensiveRead(t *testing.T) {
	store := NewStoreWithLimits(Limits{Configurations: 1, Snapshots: 1})
	snapshot := snapshotFixture(t)
	outcome, err := store.RegisterSnapshot(context.Background(), snapshot)
	if err != nil || outcome.Status != portfoliostorage.RegistrationCommitted {
		t.Fatalf("register snapshot = %#v, %v", outcome, err)
	}
	outcome, err = store.RegisterSnapshot(context.Background(), snapshot)
	if err != nil || outcome.Status != portfoliostorage.RegistrationIdempotent {
		t.Fatalf("idempotent snapshot = %#v, %v", outcome, err)
	}
	stored, err := store.Snapshot(context.Background(), snapshot.ID())
	if err != nil {
		t.Fatal(err)
	}
	raw := stored.CanonicalJSON()
	raw[0] = 'x'
	again, _ := store.Snapshot(context.Background(), snapshot.ID())
	if again.CanonicalJSON()[0] == 'x' {
		t.Fatal("returned bytes mutated stored snapshot")
	}
	values, err := store.Snapshots(context.Background(), snapshot.PortfolioID())
	if err != nil || len(values) != 1 || values[0].ID() != snapshot.ID() {
		t.Fatalf("snapshots = %#v, %v", values, err)
	}
}

func decode(t *testing.T, raw string) portfolioconfig.PortfolioConfiguration {
	t.Helper()
	value, err := portfolioconfig.Decode([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func snapshotFixture(t *testing.T) portfoliomodel.PortfolioSnapshot {
	t.Helper()
	portfolioID, _ := portfoliomodel.NewPortfolioID("repository")
	configurationID, _ := portfoliomodel.NewPortfolioConfigurationID("repository")
	hash, _ := portfoliomodel.NewConfigurationHash([]byte(`{"repository":1}`))
	source, _ := portfoliomodel.NewStateChecksum([]byte(`{"source":1}`))
	money := func(value int64) domain.Money {
		result, _ := domain.NewMoney(value, "INR")
		return result
	}
	capital, _ := portfoliomodel.NewCapitalState(money(1000), money(1000), money(0), money(0))
	date, _ := domain.NewCivilDate(2026, 7, 18)
	now := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	value, err := portfoliomodel.NewPortfolioSnapshot(portfoliomodel.PortfolioSnapshotSpec{
		SchemaVersion: "portfolio-snapshot/v1", PortfolioID: portfolioID, Revision: 1,
		AsOfExchangeTime: now, GeneratedAt: now, TradingDate: date,
		BaseCurrency: "INR", State: portfoliomodel.PortfolioEnabled,
		ConfigurationID: configurationID, ConfigurationVersion: 1,
		ConfigurationHash: hash, Capital: capital, RealizedPnL: money(0),
		UnrealizedPnL: money(0), DailyRealizedPnL: money(0),
		DailyUnrealizedPnL: money(0), WeeklyRealizedPnL: money(0),
		HighWaterMark: money(1000), CurrentEquity: money(1000),
		SourceStateChecksum: source,
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

const configurationOne = `{"schema_version":"portfolio-configuration/v1","version":1,"enabled":true,"base_currency":"INR","effective_from":"2026-01-01T00:00:00Z","effective_until":"2027-01-01T00:00:00Z","total_capital_minor":1000,"reserve_bps":1000,"emergency_reserve_bps":500,"maximum_strategy_capital_minor":500,"maximum_instrument_capital_minor":200,"maximum_underlying_capital_minor":300,"maximum_exposure_group_capital_minor":400,"maximum_strategies":10,"exposure_groups":["INDEX"]}`
const configurationTwo = `{"schema_version":"portfolio-configuration/v1","version":2,"enabled":true,"base_currency":"INR","effective_from":"2026-01-01T00:00:00Z","effective_until":"2027-01-01T00:00:00Z","total_capital_minor":2000,"reserve_bps":1000,"emergency_reserve_bps":500,"maximum_strategy_capital_minor":500,"maximum_instrument_capital_minor":200,"maximum_underlying_capital_minor":300,"maximum_exposure_group_capital_minor":400,"maximum_strategies":10,"exposure_groups":["INDEX"]}`
