package prometheus

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/telemetry"
)

func TestRecorderExposesCatalogWithoutHighCardinalityLabels(t *testing.T) {
	recorder := New()
	dimensions := telemetry.Dimensions{
		Provider: "fixture", Exchange: domain.ExchangeNSE, Segment: domain.SegmentIndex,
		Kind: model.EventKindQuote,
	}
	recorder.Observation(dimensions, "accepted")
	recorder.Quality(dimensions, model.QualityDuplicate, model.DispositionSuppressed)
	recorder.Normalization(dimensions, time.Millisecond)
	recorder.TransportLag(dimensions, time.Second)
	recorder.DatasetCommit("committed", time.Second, 100)
	recorder.Readiness("watchlist", "", "primary", "READY", "NONE", true, 1)
	response := httptest.NewRecorder()
	recorder.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	body := response.Body.String()
	for _, name := range []string{
		"tradeedge_marketdata_observations_total",
		"tradeedge_marketdata_quality_total",
		"tradeedge_marketdata_normalization_duration_seconds",
		"tradeedge_marketdata_ready",
	} {
		if !strings.Contains(body, name) {
			t.Fatalf("metrics output missing %s", name)
		}
	}
	for _, prohibited := range []string{"instrument_id=", "provider_token=", "dataset_id=", "error="} {
		if strings.Contains(body, prohibited) {
			t.Fatalf("metrics output contains prohibited label %q", prohibited)
		}
	}
	families, err := recorder.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	buckets := map[string]int{
		"tradeedge_marketdata_normalization_duration_seconds":  11,
		"tradeedge_marketdata_transport_lag_seconds":           12,
		"tradeedge_marketdata_dataset_commit_duration_seconds": 12,
	}
	for _, family := range families {
		want, found := buckets[family.GetName()]
		if !found {
			continue
		}
		if len(family.Metric) == 0 || family.Metric[0].Histogram == nil ||
			len(family.Metric[0].Histogram.Bucket) != want {
			t.Fatalf("%s bucket count is not %d", family.GetName(), want)
		}
		delete(buckets, family.GetName())
	}
	if len(buckets) != 0 {
		t.Fatalf("histogram families not gathered: %#v", buckets)
	}
}

func TestRecorderSupportsConcurrentUpdates(t *testing.T) {
	recorder := New()
	dimensions := telemetry.Dimensions{Provider: "fixture", Kind: model.EventKindQuote}
	var wait sync.WaitGroup
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				recorder.Observation(dimensions, "accepted")
				recorder.Readiness("watchlist", "", "primary", "READY", "NONE", true, 1)
			}
		}()
	}
	wait.Wait()
	if _, err := recorder.Registry().Gather(); err != nil {
		t.Fatal(err)
	}
}
