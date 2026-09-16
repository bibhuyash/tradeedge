package shadowruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/derivatives"
	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	"github.com/bibhuyash/tradeedge/internal/qualification"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
)

// This accelerated synthetic session deliberately lacks derivative mappings.
// Completed warmup and EMA crossovers must not bypass selection or reach risk.
// It writes no artifacts and cannot count as a real-market session.
func TestMockFullSessionMissingMappingsNeverReachesRisk(t *testing.T) {
	nifty, bank := testIndex(t, "NIFTY", "NIFTY 50"), testIndex(t, "BANKNIFTY", "NIFTY BANK")
	master, err := instrumentmaster.New(fixtureAt.Add(-time.Hour), []domain.Instrument{nifty, bank}, nil)
	if err != nil {
		t.Fatal(err)
	}
	niftyPolicy, _ := derivatives.PolicyFor("NIFTY")
	bankPolicy, _ := derivatives.PolicyFor("BANKNIFTY")
	engine, err := qualification.New(qualification.DefaultPolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	risk := &forbiddenSessionRisk{}
	runtime, err := New(RuntimeConfig{Master: master, SpotIDs: map[qualification.Underlying]domain.InstrumentID{qualification.NIFTY: nifty.ID(), qualification.BANKNIFTY: bank.ID()}, Policies: map[qualification.Underlying]derivatives.Policy{qualification.NIFTY: niftyPolicy, qualification.BANKNIFTY: bankPolicy}, Qualification: engine, Risk: risk, TradingDate: "2026-08-18", StartedAt: fixtureAt, TelegramAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	blocks := 0
	for minute := 0; minute <= 375; minute++ {
		// Fall through warmup, then rise sharply: a deterministic long crossover.
		price := int64(2_480_000 - minute*100)
		if minute >= 60 {
			price = int64(2_500_000 + minute*100)
		}
		for _, instrument := range []domain.Instrument{nifty, bank} {
			quote := testQuote(t, instrument, fixtureAt.Add(time.Duration(minute)*time.Minute), price)
			err = runtime.Process(context.Background(), quote, "NORMAL_TRADING", false, false)
			if errors.Is(err, ErrNotReady) {
				blocks++
			} else if err != nil {
				t.Fatal(err)
			}
		}
	}
	if blocks < 2 || risk.calls != 0 {
		t.Fatalf("selection bypass: blocks=%d risk=%d", blocks, risk.calls)
	}
	for _, status := range runtime.Status() {
		if status.WarmupSamples < 50 {
			t.Fatalf("warmup incomplete: %+v", status)
		}
	}
	if err = runtime.CloseSession(fixtureAt.Add(375*time.Minute), true); err != nil {
		t.Fatal(err)
	}
	for _, card := range runtime.SessionScorecards() {
		if card.Quality != SessionInvalid || card.AcceptedSignals != 0 || card.CompletedShadowObservations != 0 {
			t.Fatalf("unsafe scorecard: %+v", card)
		}
	}
}

type forbiddenSessionRisk struct{ calls int }

func (r *forbiddenSessionRisk) Evaluate(context.Context, derivatives.ConnectedRequest) (riskmodel.PortfolioRiskDecision, error) {
	r.calls++
	return riskmodel.PortfolioRiskDecision{}, errors.New("risk must not be reached with missing derivative mappings")
}
