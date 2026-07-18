package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

var (
	ErrDatasetNotFound     = errors.New("market-data dataset not found")
	ErrDatasetSealed       = errors.New("market-data dataset is sealed")
	ErrDatasetCorrupt      = errors.New("market-data dataset is corrupt")
	ErrEventsOutOfOrder    = errors.New("market-data events are out of order")
	ErrInvalidLineage      = errors.New("invalid market-data dataset lineage")
	ErrPublicationConflict = errors.New("market-data publication conflict")
)

type DatasetID string

type DraftManifest struct {
	ParentID         DatasetID
	Revision         int64
	MasterVersion    string
	CalendarVersion  string
	InstrumentMaster []byte
	Source           string
	SourceSHA256     string
	OrderingVersion  string
	BuildKey         string
	CorrectionReason string
	RequestID        string
	Series           string
	CreatedAt        time.Time
}

type DatasetManifest struct {
	SchemaVersion    int
	ID               DatasetID
	ParentID         DatasetID
	Revision         int64
	MasterVersion    string
	CalendarVersion  string
	Source           string
	SourceSHA256     string
	OrderingVersion  string
	BuildKey         string
	CorrectionReason string
	RequestID        string
	Series           string
	CreatedAt        time.Time
	Start            time.Time
	End              time.Time
	EventCount       int64
	QualityCount     int64
	EventsSHA256     string
	QualitySHA256    string
	MasterSHA256     string
}

type PublicationAction string

const (
	PublicationPublish  PublicationAction = "PUBLISH"
	PublicationRollback PublicationAction = "ROLLBACK"
)

type Publication struct {
	Series            string            `json:"series"`
	Generation        int64             `json:"generation"`
	DatasetID         DatasetID         `json:"dataset_id"`
	PreviousDatasetID DatasetID         `json:"previous_dataset_id,omitempty"`
	Action            PublicationAction `json:"action"`
	Reason            string            `json:"reason"`
	RequestID         string            `json:"request_id"`
	PublishedAt       time.Time         `json:"published_at"`
}

type PublicationRequest struct {
	Series            string
	DatasetID         DatasetID
	ExpectedCurrentID DatasetID
	Action            PublicationAction
	Reason            string
	RequestID         string
	PublishedAt       time.Time
}

type EventQuery struct {
	InstrumentIDs []domain.InstrumentID
	Start         time.Time
	End           time.Time
}

func (q EventQuery) Includes(event model.Event) bool {
	if !q.Start.IsZero() && event.ExchangeTime().Before(q.Start) {
		return false
	}
	if !q.End.IsZero() && !event.ExchangeTime().Before(q.End) {
		return false
	}
	if len(q.InstrumentIDs) == 0 {
		return true
	}
	for _, id := range q.InstrumentIDs {
		if event.InstrumentID() == id {
			return true
		}
	}
	return false
}

type EventSink func(context.Context, model.Event) error

type DatasetRepository interface {
	Create(ctx context.Context, manifest DraftManifest) (DatasetWriter, error)
	Open(ctx context.Context, id DatasetID) (DatasetReader, error)
}

type RevisionRepository interface {
	DatasetRepository
	Lineage(ctx context.Context, id DatasetID, limit int) ([]DatasetManifest, error)
	Publish(ctx context.Context, request PublicationRequest) (Publication, error)
	CurrentPublication(ctx context.Context, series string) (Publication, error)
	Publications(ctx context.Context, series string, limit int) ([]Publication, error)
}

type DatasetWriter interface {
	Append(ctx context.Context, event model.Event) error
	RecordQuality(ctx context.Context, record model.QualityRecord) error
	Commit(ctx context.Context) (DatasetManifest, error)
	Abort(ctx context.Context) error
}

type DatasetReader interface {
	Manifest() DatasetManifest
	Scan(ctx context.Context, query EventQuery, sink EventSink) error
	Close() error
}

func NormalizeDraft(draft DraftManifest, parent *DatasetManifest) (DraftManifest, error) {
	draft.Source = strings.TrimSpace(draft.Source)
	draft.MasterVersion = strings.TrimSpace(draft.MasterVersion)
	draft.CalendarVersion = strings.TrimSpace(draft.CalendarVersion)
	draft.OrderingVersion = strings.TrimSpace(draft.OrderingVersion)
	draft.CorrectionReason = strings.TrimSpace(draft.CorrectionReason)
	draft.RequestID = strings.TrimSpace(draft.RequestID)
	draft.Series = strings.TrimSpace(draft.Series)
	if draft.MasterVersion == "" || draft.CalendarVersion == "" || draft.Source == "" ||
		draft.OrderingVersion == "" || draft.CreatedAt.IsZero() {
		return DraftManifest{}, ErrDatasetCorrupt
	}
	if draft.SourceSHA256 == "" {
		digest := sha256.Sum256([]byte(draft.Source))
		draft.SourceSHA256 = hex.EncodeToString(digest[:])
	}
	if len(draft.SourceSHA256) != sha256.Size*2 {
		return DraftManifest{}, ErrDatasetCorrupt
	}
	if parent == nil {
		if draft.ParentID != "" {
			return DraftManifest{}, ErrInvalidLineage
		}
		if draft.Revision == 0 {
			draft.Revision = 1
		}
		if draft.Revision != 1 {
			return DraftManifest{}, ErrInvalidLineage
		}
	} else {
		if draft.ParentID != parent.ID {
			return DraftManifest{}, ErrInvalidLineage
		}
		expected := parent.Revision + 1
		if draft.Revision == 0 {
			draft.Revision = expected
		}
		if draft.Revision != expected || draft.CorrectionReason == "" || draft.RequestID == "" {
			return DraftManifest{}, ErrInvalidLineage
		}
	}
	if draft.BuildKey == "" {
		draft.BuildKey = ComputeBuildKey(draft)
	}
	return draft, nil
}

func ComputeBuildKey(draft DraftManifest) string {
	payload := fmt.Sprintf("v1|%s|%d|%s|%s|%s|%s|%s|%s",
		draft.ParentID, draft.Revision, draft.MasterVersion, draft.CalendarVersion,
		draft.SourceSHA256, draft.OrderingVersion, draft.RequestID, draft.Series)
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}
