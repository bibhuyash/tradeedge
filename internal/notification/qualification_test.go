package notification

import (
	"strings"
	"testing"
	"time"
)

func TestShadowQualificationMessagesAreExplicitlyNonBroker(t *testing.T) {
	for _, tc := range []struct {
		kind   Kind
		marker string
	}{{KindShadowQualification, "TRADEEDGE SHADOW"}, {KindShadowQualificationResult, "TRADEEDGE SHADOW RESULT"}} {
		event, err := NewEvent(EventSpec{SourceID: "qualification-1", TradingDate: "2026-08-17", Mode: "SHADOW", OccurredAt: time.Date(2026, 8, 17, 4, 30, 0, 0, time.UTC), Category: CategoryStrategy, Kind: tc.kind, Severity: SeverityInfo, Details: Details{Subject: "Broker Order: NONE", State: "NOT_ALPHA_QUALIFIED"}})
		if err != nil {
			t.Fatal(err)
		}
		text := Render(event).Text
		if !strings.Contains(text, tc.marker) || !strings.Contains(text, "Broker Order: NONE") || strings.Contains(strings.ToUpper(text), "BUY NOW") {
			t.Fatalf("unsafe message: %s", text)
		}
	}
}
