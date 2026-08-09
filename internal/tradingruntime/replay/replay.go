// Package replay drives the production Phase 7 runtime with deterministic
// inputs and synchronous backpressure.
package replay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/tradingruntime"
)

type Runtime interface {
	Process(context.Context, marketmodel.Event) (tradingruntime.EventReceipt, error)
	Snapshot() tradingruntime.RuntimeSnapshot
}

type Result struct {
	Receipts []tradingruntime.EventReceipt  `json:"receipts"`
	Final    tradingruntime.RuntimeSnapshot `json:"final"`
	Checksum string                         `json:"checksum"`
}

func Run(ctx context.Context, runtime Runtime, events []marketmodel.Event) (Result, error) {
	result := Result{Receipts: make([]tradingruntime.EventReceipt, 0, len(events))}
	for _, event := range events {
		receipt, err := runtime.Process(ctx, event)
		if err != nil {
			return Result{}, err
		}
		result.Receipts = append(result.Receipts, receipt)
	}
	result.Final = runtime.Snapshot()
	raw, _ := json.Marshal(struct {
		Receipts []tradingruntime.EventReceipt  `json:"receipts"`
		Final    tradingruntime.RuntimeSnapshot `json:"final"`
	}{result.Receipts, result.Final})
	sum := sha256.Sum256(raw)
	result.Checksum = hex.EncodeToString(sum[:])
	return result, nil
}
