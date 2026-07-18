package storage

import (
	"testing"
	"time"
)

func FuzzCorrectionMetadata(f *testing.F) {
	f.Add("reason", "request", "series")
	f.Fuzz(func(t *testing.T, reason, requestID, series string) {
		parent := DatasetManifest{ID: "parent", Revision: 1}
		_, _ = NormalizeDraft(DraftManifest{
			ParentID: "parent", MasterVersion: "master", CalendarVersion: "calendar",
			Source: "fixture", OrderingVersion: "ordering", CreatedAt: time.Unix(1, 0),
			CorrectionReason: reason, RequestID: requestID, Series: series,
		}, &parent)
	})
}
