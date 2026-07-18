package file

import (
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/marketdata/storage"
)

func FuzzPublicationValidation(f *testing.F) {
	f.Add("series", "dataset", "reason", "request")
	f.Fuzz(func(t *testing.T, series, dataset, reason, request string) {
		_ = validatePublicationRequest(storage.PublicationRequest{
			Series: series, DatasetID: storage.DatasetID(dataset), Reason: reason,
			RequestID: request, Action: storage.PublicationPublish, PublishedAt: time.Unix(1, 0),
		})
	})
}
