package opshttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bibhuyash/tradeedge/internal/derivatives"
)

type source struct{}

func (source) Snapshot() derivatives.Snapshot {
	return derivatives.Snapshot{SchemaVersion: derivatives.MachineSchemaVersion, Mode: derivatives.ModeShadow, MasterVersion: "master"}
}
func TestHandlerIsBoundedAndGetOnly(t *testing.T) {
	h := New(source{})
	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/derivatives/status", nil))
	if get.Code != http.StatusOK || get.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET failed: %d", get.Code)
	}
	post := httptest.NewRecorder()
	h.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/api/v1/derivatives/status", nil))
	if post.Code != http.StatusNotFound || post.Header().Get("Allow") != http.MethodGet {
		t.Fatal("mutation method was not denied")
	}
}
