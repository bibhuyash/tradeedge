package model

import (
	"context"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

func TestStaticCalendarUsesExplicitSessionBoundaries(t *testing.T) {
	location, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatalf("LoadLocation() error = %v", err)
	}
	date, _ := domain.NewCivilDate(2026, time.July, 17)
	open := time.Date(2026, time.July, 17, 9, 15, 0, 0, location)
	closeTime := time.Date(2026, time.July, 17, 15, 30, 0, 0, location)
	calendar, err := NewStaticCalendar([]MarketSession{{
		Exchange: domain.ExchangeNSE, TradingDate: date, Open: open, Close: closeTime, Version: "test-v1",
	}})
	if err != nil {
		t.Fatalf("NewStaticCalendar() error = %v", err)
	}
	session, active, err := calendar.SessionAt(context.Background(), domain.ExchangeNSE, open.Add(time.Minute))
	if err != nil || !active || !session.Contains(open) || session.Contains(closeTime) {
		t.Fatalf("SessionAt() session=%#v active=%v error=%v", session, active, err)
	}
	if _, active, _ := calendar.SessionAt(context.Background(), domain.ExchangeNSE, closeTime.Add(time.Second)); active {
		t.Fatal("calendar reports active after close")
	}
}
