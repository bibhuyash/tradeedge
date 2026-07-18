package model

import (
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

type QualityCode string

const (
	QualityValid     QualityCode = "VALID"
	QualityDuplicate QualityCode = "DUPLICATE"
	QualityLate      QualityCode = "LATE"
	QualityStale     QualityCode = "STALE"
	QualityMissing   QualityCode = "MISSING"
	QualityMalformed QualityCode = "MALFORMED"
	QualityCorrected QualityCode = "CORRECTED"
)

type Disposition string

const (
	DispositionAccepted    Disposition = "ACCEPTED"
	DispositionSuppressed  Disposition = "SUPPRESSED"
	DispositionQuarantined Disposition = "QUARANTINED"
)

type QualityRecord struct {
	Code            QualityCode
	Disposition     Disposition
	Provider        domain.Provider
	InstrumentID    domain.InstrumentID
	ExchangeTime    time.Time
	ObservedAt      time.Time
	Reason          string
	SourcePosition  int64
	DatasetRevision string
}

type DataState string

const (
	DataNoData        DataState = "NO_DATA"
	DataCurrent       DataState = "CURRENT"
	DataStale         DataState = "DATA_STALE"
	DataSessionClosed DataState = "SESSION_CLOSED"
)
