package httpserver

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConsoleMountedWithoutEnablingMockOrNewAPIs(t *testing.T) {
	handler := NewHandler(&Readiness{})
	for _, test := range []struct {
		path, contains string
		code           int
	}{
		{"/console/", "TradeEdge", 200},
		{"/console/config.json?scenario=healthy", "\"mock\":false", 200},
		{"/api/v1/shadow/runtime", "404", 404},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", test.path, nil))
		if w.Code != test.code || !strings.Contains(w.Body.String(), test.contains) {
			t.Fatalf("%s: %d %s", test.path, w.Code, w.Body.String())
		}
	}
}
