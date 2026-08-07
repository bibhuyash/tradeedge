package replay

import (
	"context"
	"errors"
	"sort"

	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	accountingstorage "github.com/bibhuyash/tradeedge/internal/accounting/storage"
)

var ErrInvalidReplay = errors.New("invalid accounting replay")

type Applier interface {
	ApplyFill(context.Context, accountingmodel.AccountingFill) (accountingstorage.PublicationReceipt, error)
}
type Engine struct{ applier Applier }

func New(applier Applier) (*Engine, error) {
	if applier == nil {
		return nil, ErrInvalidReplay
	}
	return &Engine{applier}, nil
}

func (engine *Engine) Replay(ctx context.Context, fills []accountingmodel.AccountingFill) ([]accountingstorage.PublicationReceipt, error) {
	ordered := append([]accountingmodel.AccountingFill(nil), fills...)
	for _, fill := range ordered {
		if fill.IsZero() {
			return nil, ErrInvalidReplay
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		left, right := ordered[i], ordered[j]
		if left.PositionID() != right.PositionID() {
			return left.PositionID().String() < right.PositionID().String()
		}
		return left.OrderingKey().Compare(right.OrderingKey()) < 0
	})
	receipts := make([]accountingstorage.PublicationReceipt, 0, len(ordered))
	for _, fill := range ordered {
		receipt, err := engine.applier.ApplyFill(ctx, fill)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, receipt)
	}
	return receipts, nil
}
