package notification

import (
	"context"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

type NotificationProvider interface {
	Notify(ctx context.Context, notification domain.Notification) error
}
