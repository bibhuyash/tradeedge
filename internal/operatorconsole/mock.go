package operatorconsole

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/bibhuyash/tradeedge/internal/qualification"
	"github.com/bibhuyash/tradeedge/internal/shadowruntime"
	shadowhttp "github.com/bibhuyash/tradeedge/internal/shadowruntime/opshttp"
)

// NewMock is used only by the standalone offline demo. No credentials, network
// clients, domain execution ports or evidence writers are part of this fixture.
func NewMock() http.Handler {
	assets := New()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", 405)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-TradeEdge-Data-Source", "MOCK")
		respond := func(code int, body any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(body)
		}
		if r.URL.Path == "/console/config.json" {
			respond(200, map[string]bool{"mock": true})
			return
		}
		if len(r.URL.Path) >= 9 && r.URL.Path[:9] == "/console/" {
			assets.ServeHTTP(w, r)
			return
		}
		scenario := r.URL.Query().Get("scenario")
		if scenario == "" {
			scenario = "healthy"
		}
		if scenario != "healthy" && scenario != "warming" && scenario != "degraded" && scenario != "empty" {
			respond(400, map[string]string{"error": "unknown mock scenario"})
			return
		}
		switch r.URL.Path {
		case "/healthz":
			respond(200, map[string]string{"status": "ok"})
		case "/readyz":
			code, state, status := 200, "READY", "ready"
			if scenario == "degraded" {
				code, state, status = 503, "STALE", "not_ready"
			}
			if scenario == "empty" {
				code, state, status = 503, "WARMING", "not_ready"
			}
			respond(code, map[string]any{"status": status, "market_data_state": state, "trading_permitted": false, "mock": true})
		case "/api/v1/integrations/zerodha/status":
			status, code := "MOCK_CONNECTED", 200
			if scenario == "degraded" {
				status, code = "MOCK_DISCONNECTED", 503
			}
			respond(code, map[string]any{"mode": "SHADOW", "read_only": true, "status": status, "mock": true, "broker_mutations": 0})
		case "/api/v1/notifications/health":
			status, code := "MOCK_AVAILABLE", 200
			if scenario == "degraded" {
				status, code = "MOCK_UNAVAILABLE", 503
			}
			respond(code, map[string]any{"status": status, "mock": true})
		case "/api/v1/shadow/runtime", "/api/v1/shadow/scorecards":
			handler, err := shadowhttp.New(mockSource{scenario: scenario})
			if err != nil {
				http.Error(w, "mock unavailable", 500)
				return
			}
			handler.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

type mockSource struct{ scenario string }

func (s mockSource) Snapshot() shadowruntime.Snapshot                           { return shadowruntime.Snapshot{Revision: 1} }
func (s mockSource) RecentEvaluations(int) ([]shadowruntime.Evaluation, uint64) { return nil, 0 }
func (s mockSource) Status() []shadowruntime.UnderlyingStatus {
	result := []shadowruntime.UnderlyingStatus{}
	if s.scenario == "empty" {
		return result
	}
	for _, u := range []qualification.Underlying{qualification.NIFTY, qualification.BANKNIFTY} {
		value := shadowruntime.UnderlyingStatus{Underlying: u, MarketData: "READY", Future: "READY", OptionUniverse: "READY", Strategy: "READY", WarmupSamples: 50, WarmupRequired: 50, SelectedOption: "MOCK_" + string(u) + "_CALL", LastRisk: "NO_DECISION"}
		if s.scenario == "warming" {
			value.WarmupSamples = 6
			value.Strategy = "WARMING"
			value.OptionUniverse = "WARMING"
			value.SelectedOption = ""
		}
		if s.scenario == "degraded" {
			value.MarketData = "STALE"
			value.Strategy = "BLOCKED"
			value.LastRisk = "BLOCKED"
			value.LastRiskReason = "MARKET_DATA_STALE"
		}
		result = append(result, value)
	}
	return result
}
func (s mockSource) SessionScorecards() []shadowruntime.SessionScorecard {
	result := []shadowruntime.SessionScorecard{}
	if s.scenario == "empty" {
		return result
	}
	for _, u := range []qualification.Underlying{qualification.NIFTY, qualification.BANKNIFTY} {
		value := shadowruntime.SessionScorecard{SchemaVersion: shadowruntime.SchemaVersion, SessionID: "MOCK_" + string(u), TradingDate: "2026-08-18", Underlying: u, StrategyID: "EMA_REFERENCE_V1", StrategyVersion: "1", StartedAt: time.Date(2026, 8, 18, 3, 45, 0, 0, time.UTC), Quality: shadowruntime.SessionCollecting, NetPnL: "NOT_AVAILABLE", TelegramAvailable: true}
		if s.scenario == "healthy" {
			value.Signals = 3
			value.AcceptedSignals = 2
			value.RiskRejectedSignals = 1
		}
		if s.scenario == "degraded" {
			value.Quality = shadowruntime.SessionInvalid
			value.DataGaps = 2
			value.TelegramAvailable = false
			value.Reasons = []shadowruntime.SessionReason{shadowruntime.ReasonMarketDataGap, shadowruntime.ReasonTelegramOutage}
		}
		result = append(result, value)
	}
	return result
}
func (s mockSource) MultiSessionScorecards() []shadowruntime.MultiSessionScorecard { return nil }
