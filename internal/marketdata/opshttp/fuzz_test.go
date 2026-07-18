package opshttp

import (
	"net/http/httptest"
	"net/url"
	"testing"
)

func FuzzOperationalQueryParsing(f *testing.F) {
	f.Add("100", "0")
	f.Add("251", "-1")
	f.Fuzz(func(t *testing.T, limit, cursor string) {
		request := httptest.NewRequest("GET", "/api/v1/market-data/readiness/instruments?limit="+
			url.QueryEscape(limit)+"&cursor="+url.QueryEscape(cursor), nil)
		_, _, _ = parsePage(request)
	})
}
