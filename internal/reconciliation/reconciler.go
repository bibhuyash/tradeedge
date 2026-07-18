package reconciliation

import (
	"context"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

type Reconciler interface {
	Reconcile(ctx context.Context, accountID domain.AccountID) (domain.ReconciliationReport, error)
}
