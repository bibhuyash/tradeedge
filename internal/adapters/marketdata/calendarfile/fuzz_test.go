package calendarfile

import "testing"

func FuzzDecode(f *testing.F) {
	f.Add([]byte(`{"schema_version":1,"source":"fuzz","published_at":"2026-07-01T00:00:00Z","timezone":"Asia/Kolkata","effective_from":"2026-07-18","effective_to":"2026-07-18","days":[{"exchange":"NSE","date":"2026-07-18","status":"HOLIDAY"}]}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = Decode(data)
	})
}
