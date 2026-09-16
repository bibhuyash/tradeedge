package operatorconsole

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAssetsAreReadOnlyAndProtected(t *testing.T) {
	h := New()
	for _, path := range []string{"/console/", "/console/app.js", "/console/styles.css", "/console/export.css", "/console/config.json"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || w.Body.Len() == 0 || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("asset %s: %d", path, w.Code)
		}
	}
	for _, v := range []struct {
		method, path string
		status       int
	}{{"POST", "/console/", 405}, {"GET", "/console/missing", 404}, {"GET", "/api/v1/shadow/runtime", 404}, {"HEAD", "/console/", 200}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(v.method, v.path, nil))
		if w.Code != v.status {
			t.Fatalf("%v: %d", v, w.Code)
		}
		if v.method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("HEAD returned a body")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/console/config.json?scenario=healthy", nil))
	if strings.TrimSpace(w.Body.String()) != "{\"mock\":false}" {
		t.Fatal("production can enable mock mode")
	}
}

func TestMockScenariosUseShadowContractAndNeverPermitTrading(t *testing.T) {
	h := NewMock()
	for _, scenario := range []string{"healthy", "warming", "degraded", "empty"} {
		t.Run(scenario, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz?scenario="+scenario, nil))
			var ready map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &ready); err != nil {
				t.Fatal(err)
			}
			if ready["trading_permitted"] != false || w.Header().Get("X-TradeEdge-Data-Source") != "MOCK" {
				t.Fatal("unsafe mock")
			}
			if (scenario == "degraded" || scenario == "empty") && w.Code != 503 {
				t.Fatal("failed to degrade")
			}
			w = httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/shadow/runtime?scenario="+scenario, nil))
			var runtime struct {
				Mode, BrokerOrders string
				Status             []struct {
					WarmupSamples int    `json:"warmup_samples"`
					MarketData    string `json:"market_data"`
				}
			}
			if err := json.Unmarshal(w.Body.Bytes(), &runtime); err != nil {
				t.Fatal(err)
			}
			if runtime.Mode != "SHADOW" || runtime.BrokerOrders != "DISABLED" {
				t.Fatal("mock escaped SHADOW")
			}
			if scenario == "empty" {
				if len(runtime.Status) != 0 {
					t.Fatal("not empty")
				}
			} else if len(runtime.Status) != 2 {
				t.Fatal("missing underlyings")
			}
			if scenario == "warming" && runtime.Status[0].WarmupSamples != 6 {
				t.Fatal("wrong warmup")
			}
			if scenario == "degraded" && runtime.Status[0].MarketData != "STALE" {
				t.Fatal("stale not visible")
			}
			for _, path := range []string{"/api/v1/shadow/scorecards", "/api/v1/integrations/zerodha/status", "/api/v1/notifications/health"} {
				w = httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("GET", path+"?scenario="+scenario, nil))
				if !json.Valid(w.Body.Bytes()) {
					t.Fatalf("invalid JSON: %s", path)
				}
			}
		})
	}
	for _, v := range []struct {
		method, path string
		code         int
	}{{"POST", "/api/v1/orders", 405}, {"GET", "/api/v1/orders", 404}, {"GET", "/readyz?scenario=invalid", 400}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(v.method, v.path, nil))
		if w.Code != v.code {
			t.Fatalf("%v got %d", v, w.Code)
		}
	}
}
