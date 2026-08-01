package replay

import (
	"context"
	"errors"

	"github.com/bibhuyash/tradeedge/internal/risk/runner"
)

var ErrInvalidReplay = errors.New("invalid portfolio risk replay")

type Evaluator interface {
	EvaluateProposal(context.Context, runner.Request) (runner.Receipt, error)
}

type Engine struct{ evaluator Evaluator }

func New(evaluator Evaluator) (*Engine, error) {
	if evaluator == nil {
		return nil, ErrInvalidReplay
	}
	return &Engine{evaluator: evaluator}, nil
}

func (engine *Engine) Run(ctx context.Context, requests []runner.Request) ([]runner.Receipt, error) {
	receipts := make([]runner.Receipt, 0, len(requests))
	for _, request := range requests {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		receipt, err := engine.evaluator.EvaluateProposal(ctx, request)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, receipt)
	}
	return receipts, nil
}
