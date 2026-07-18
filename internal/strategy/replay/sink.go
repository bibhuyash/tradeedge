package replay

import (
	"context"
	"errors"
	"sync"

	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
	"github.com/bibhuyash/tradeedge/internal/strategy/runner"
)

var ErrInvalidReplayEvent = errors.New("invalid strategy replay event")

type Evaluator interface {
	EvaluateFrame(context.Context, domain.StrategyID, strategymodel.CandleFrame) (runner.Receipt, error)
}

type Sink struct {
	mu              sync.Mutex
	evaluator       Evaluator
	instanceID      domain.StrategyID
	subscriptions   strategymodel.SubscriptionSpec
	calendarVersion string
	buffers         map[string][]marketmodel.CompletedCandleEvent
	receipts        []runner.Receipt
}

func NewSink(
	evaluator Evaluator,
	instanceID domain.StrategyID,
	subscriptions strategymodel.SubscriptionSpec,
	calendarVersion string,
) (*Sink, error) {
	if evaluator == nil || instanceID == "" || subscriptions.IsZero() || calendarVersion == "" {
		return nil, ErrInvalidReplayEvent
	}
	return &Sink{
		evaluator: evaluator, instanceID: instanceID, subscriptions: subscriptions,
		calendarVersion: calendarVersion,
		buffers:         make(map[string][]marketmodel.CompletedCandleEvent),
	}, nil
}

func (sink *Sink) Consume(ctx context.Context, event marketmodel.Event) error {
	candle, ok := event.(marketmodel.CompletedCandleEvent)
	if !ok {
		return nil
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var trigger bool
	for _, subscription := range sink.subscriptions.Subscriptions() {
		if subscription.InstrumentID != candle.InstrumentID() ||
			subscription.Interval != candle.Interval() {
			continue
		}
		values := append(sink.buffers[subscription.Role], candle)
		if len(values) > subscription.Lookback {
			values = append([]marketmodel.CompletedCandleEvent(nil),
				values[len(values)-subscription.Lookback:]...)
		}
		sink.buffers[subscription.Role] = values
		trigger = trigger || subscription.Trigger
	}
	if !trigger {
		return nil
	}
	series := make([]strategymodel.CandleSeries, 0)
	for _, subscription := range sink.subscriptions.Subscriptions() {
		values := sink.buffers[subscription.Role]
		if subscription.Required && len(values) == 0 {
			return nil
		}
		value, err := strategymodel.NewCandleSeries(subscription, values)
		if err != nil {
			return err
		}
		series = append(series, value)
	}
	frameTrigger, _ := strategymodel.NewTriggerID("strategy-replay-frame/v1|" + candle.ID().String())
	frame, err := strategymodel.NewCandleFrame(strategymodel.CandleFrameSpec{
		TriggerID: frameTrigger, LogicalTime: candle.CloseTime(),
		Subscription: sink.subscriptions, Series: series,
		MasterVersion:   candle.Provenance().MasterVersion,
		CalendarVersion: sink.calendarVersion,
		DatasetRevision: candle.Provenance().DatasetRevision,
	})
	if err != nil {
		return err
	}
	receipt, err := sink.evaluator.EvaluateFrame(ctx, sink.instanceID, frame)
	if err != nil {
		return err
	}
	sink.receipts = append(sink.receipts, receipt)
	return nil
}

func (sink *Sink) Receipts() []runner.Receipt {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]runner.Receipt(nil), sink.receipts...)
}
