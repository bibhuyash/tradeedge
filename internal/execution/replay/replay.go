package replay

import (
	"context"
	"errors"
	"time"

	"github.com/bibhuyash/tradeedge/internal/execution/coordinator"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
)

var ErrInvalidReplay = errors.New("invalid execution replay")

type Executor interface {
	ExecutePlan(context.Context, executionmodel.OrderPlanID, time.Time) (coordinator.PlanReceipt, error)
	ResumePlan(context.Context, executionmodel.OrderPlanID, time.Time) (coordinator.PlanReceipt, error)
}
type Step struct {
	PlanID      executionmodel.OrderPlanID
	LogicalTime time.Time
	Resume      bool
}
type Engine struct{ executor Executor }

func New(executor Executor) (*Engine, error) {
	if executor == nil {
		return nil, ErrInvalidReplay
	}
	return &Engine{executor}, nil
}
func (engine *Engine) Run(ctx context.Context, steps []Step) ([]coordinator.PlanReceipt, error) {
	receipts := make([]coordinator.PlanReceipt, 0, len(steps))
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var receipt coordinator.PlanReceipt
		var err error
		if step.Resume {
			receipt, err = engine.executor.ResumePlan(ctx, step.PlanID, step.LogicalTime)
		} else {
			receipt, err = engine.executor.ExecutePlan(ctx, step.PlanID, step.LogicalTime)
		}
		if err != nil && !errors.Is(err, coordinator.ErrUnknownOutcome) {
			return nil, err
		}
		receipts = append(receipts, receipt)
	}
	return receipts, nil
}
