package operations

import (
	"context"
	"net/http"
	"time"

	"github.com/bibhuyash/tradeedge/internal/notification"
	"github.com/bibhuyash/tradeedge/internal/operations/cas"
	"github.com/bibhuyash/tradeedge/internal/operations/opshttp"
	"github.com/bibhuyash/tradeedge/internal/operations/reporting"
)

// Subsystem is the optional M2 composition root. Pass it as the trading
// runtime OperationalObserver; its errors remain isolated inside notification
// health and never flow back to trading.
type Subsystem struct {
	Dispatcher *notification.Dispatcher
	Store      *notification.Store
	CAS        *cas.Store
	Reports    *reporting.Accumulator
	observer   *Observer
	handler    http.Handler
}

func NewSubsystem(sender notification.Sender, telemetry notification.Telemetry) (*Subsystem, error) {
	store, err := notification.NewStore(1024, 2048)
	if err != nil {
		return nil, err
	}
	casStore, err := cas.NewStore(1024)
	if err != nil {
		return nil, err
	}
	casRecorder, err := cas.NewRecorder(casStore)
	if err != nil {
		return nil, err
	}
	reports, err := reporting.NewAccumulator(32)
	if err != nil {
		return nil, err
	}
	dispatcher, err := notification.NewDispatcher(notification.DefaultConfig(), sender, store, telemetry, time.Now)
	if err != nil {
		return nil, err
	}
	observer, err := NewObserver(dispatcher, casRecorder, reports, telemetry)
	if err != nil {
		return nil, err
	}
	value := &Subsystem{Dispatcher: dispatcher, Store: store, CAS: casStore, Reports: reports, observer: observer}
	value.handler = opshttp.New(opshttp.Dependencies{Notifications: dispatcher, Store: store, CAS: casStore, Reports: reports})
	return value, nil
}
func (s *Subsystem) Observe(event notification.Event)   { s.observer.Observe(event) }
func (s *Subsystem) Shutdown(ctx context.Context) error { return s.Dispatcher.Shutdown(ctx) }
func (s *Subsystem) Handler() http.Handler              { return s.handler }
