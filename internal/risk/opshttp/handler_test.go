package opshttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	riskstorage "github.com/bibhuyash/tradeedge/internal/risk/storage"
)

type testRepository struct{}

func (testRepository) CurrentPortfolioCheckpoint(context.Context, portfoliomodel.PortfolioID) (riskstorage.PortfolioCheckpoint, error) {
	return riskstorage.PortfolioCheckpoint{}, riskstorage.ErrNotFound
}
func (testRepository) RecentDecisions(context.Context, portfoliomodel.PortfolioID, int) ([]riskmodel.PortfolioRiskDecision, error) {
	return nil, nil
}

type testHealth struct{}

func (testHealth) Health() (bool, int, int, int, time.Duration) {
	return false, 0, 0, 4, 100 * time.Millisecond
}

func TestHandlerIsGETOnlyAndBounded(t *testing.T) {
	handler := New(testRepository{}, nil, testHealth{}, time.Second)
	portfolio, _ := portfoliomodel.NewPortfolioID("api-test")
	base := "/api/v1/risk/runner?portfolio=" + portfolio.String()

	request := httptest.NewRequest(http.MethodPost, base, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("POST status=%d allow=%q", response.Code, response.Header().Get("Allow"))
	}

	request = httptest.NewRequest(http.MethodGet, base+"&limit=101", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unbounded limit status=%d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/risk/runner", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid portfolio status=%d", response.Code)
	}
}
