package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

var (
	ErrInvalidEvent     = errors.New("invalid market-data event")
	ErrCurrencyMismatch = errors.New("market-data price currency mismatch")
)

type EventID [sha256.Size]byte

func NewEventID(stablePayload string) (EventID, error) {
	if stablePayload == "" {
		return EventID{}, ErrInvalidEvent
	}
	return sha256.Sum256([]byte(stablePayload)), nil
}

func ParseEventID(value string) (EventID, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return EventID{}, ErrInvalidEvent
	}
	var id EventID
	copy(id[:], decoded)
	return id, nil
}

func (id EventID) String() string { return hex.EncodeToString(id[:]) }
func (id EventID) IsZero() bool   { return id == EventID{} }

type EventKind string

const (
	EventKindQuote  EventKind = "QUOTE"
	EventKindCandle EventKind = "COMPLETED_CANDLE"
)

type Provenance struct {
	Provider        domain.Provider
	ProviderToken   string
	MasterVersion   string
	SourceSequence  uint64
	HasSequence     bool
	DatasetRevision string
}

func (p Provenance) Validate() error {
	if p.Provider == "" || p.ProviderToken == "" || p.MasterVersion == "" {
		return ErrInvalidEvent
	}
	return nil
}

type Event interface {
	ID() EventID
	Kind() EventKind
	InstrumentID() domain.InstrumentID
	ExchangeTime() time.Time
	IngestedAt() time.Time
	Provenance() Provenance
	eventMarker()
}

func StableEventKey(event Event) string {
	provenance := event.Provenance()
	sequence := ""
	if provenance.HasSequence {
		sequence = fmt.Sprintf("%020d", provenance.SourceSequence)
	}
	return fmt.Sprintf("%s|%s|%s", event.ExchangeTime().UTC().Format(time.RFC3339Nano), sequence, event.ID())
}

func EventLess(left, right Event) bool {
	if !left.ExchangeTime().Equal(right.ExchangeTime()) {
		return left.ExchangeTime().Before(right.ExchangeTime())
	}
	lp, rp := left.Provenance(), right.Provenance()
	if lp.HasSequence && rp.HasSequence && lp.SourceSequence != rp.SourceSequence {
		return lp.SourceSequence < rp.SourceSequence
	}
	return left.ID().String() < right.ID().String()
}
