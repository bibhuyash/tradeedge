package storage

import (
	"context"
	"errors"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

var (
	ErrDatasetNotFound  = errors.New("market-data dataset not found")
	ErrDatasetSealed    = errors.New("market-data dataset is sealed")
	ErrDatasetCorrupt   = errors.New("market-data dataset is corrupt")
	ErrEventsOutOfOrder = errors.New("market-data events are out of order")
)

type DatasetID string

type DraftManifest struct {
	ParentID         DatasetID
	MasterVersion    string
	InstrumentMaster []byte
	Source           string
	OrderingVersion  string
	CreatedAt        time.Time
}

type DatasetManifest struct {
	SchemaVersion   int
	ID              DatasetID
	ParentID        DatasetID
	MasterVersion   string
	Source          string
	OrderingVersion string
	CreatedAt       time.Time
	Start           time.Time
	End             time.Time
	EventCount      int64
	QualityCount    int64
	EventsSHA256    string
	QualitySHA256   string
	MasterSHA256    string
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
