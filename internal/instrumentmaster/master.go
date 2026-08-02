package instrumentmaster

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

var (
	ErrInvalidMaster      = errors.New("invalid instrument master")
	ErrInstrumentNotFound = errors.New("instrument mapping not found")
	ErrAmbiguousMapping   = errors.New("ambiguous instrument mapping")
)

type Version string

type Master struct {
	version     Version
	asOf        time.Time
	instruments map[domain.InstrumentID]domain.Instrument
	mappings    []domain.ProviderInstrumentRef
}

func New(asOf time.Time, instruments []domain.Instrument, mappings []domain.ProviderInstrumentRef) (Master, error) {
	if asOf.IsZero() || len(instruments) == 0 {
		return Master{}, ErrInvalidMaster
	}
	byID := make(map[domain.InstrumentID]domain.Instrument, len(instruments))
	keys := make([]string, 0, len(instruments)+len(mappings))
	for _, instrument := range instruments {
		if instrument.IsZero() {
			return Master{}, ErrInvalidMaster
		}
		if _, exists := byID[instrument.ID()]; exists {
			return Master{}, ErrInvalidMaster
		}
		byID[instrument.ID()] = instrument
		keys = append(keys, "instrument:"+instrument.ID().String())
	}
	for _, mapping := range mappings {
		if err := mapping.Validate(); err != nil {
			return Master{}, ErrInvalidMaster
		}
		if _, exists := byID[mapping.InstrumentID]; !exists {
			return Master{}, ErrInvalidMaster
		}
		keys = append(keys, fmt.Sprintf("mapping:%s:%s:%s:%s:%s:%s",
			mapping.Provider, mapping.Token, mapping.TradingSymbol, mapping.InstrumentID,
			mapping.ValidFrom.UTC().Format(time.RFC3339Nano),
			mapping.ValidUntil.UTC().Format(time.RFC3339Nano)))
	}
	for left := 0; left < len(mappings); left++ {
		for right := left + 1; right < len(mappings); right++ {
			if mappings[left].Provider != mappings[right].Provider ||
				!intervalsOverlap(mappings[left].ValidFrom, mappings[left].ValidUntil, mappings[right].ValidFrom, mappings[right].ValidUntil) {
				continue
			}
			if mappings[left].Token == mappings[right].Token || mappings[left].InstrumentID == mappings[right].InstrumentID {
				return Master{}, ErrAmbiguousMapping
			}
		}
	}
	sort.Strings(keys)
	digest := sha256.Sum256([]byte("v1|" + asOf.UTC().Format(time.RFC3339Nano) + "|" + strings.Join(keys, "|")))
	version := Version(hex.EncodeToString(digest[:]))
	copiedMappings := append([]domain.ProviderInstrumentRef(nil), mappings...)
	for index := range copiedMappings {
		copiedMappings[index].MasterVersion = string(version)
	}
	return Master{
		version:     version,
		asOf:        asOf.UTC(),
		instruments: byID,
		mappings:    copiedMappings,
	}, nil
}

func (m Master) Version() Version { return m.version }
func (m Master) AsOf() time.Time  { return m.asOf }

func (m Master) Instrument(id domain.InstrumentID) (domain.Instrument, bool) {
	instrument, found := m.instruments[id]
	return instrument, found
}

func (m Master) Instruments() []domain.Instrument {
	result := make([]domain.Instrument, 0, len(m.instruments))
	for _, instrument := range m.instruments {
		result = append(result, instrument)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID().String() < result[j].ID().String()
	})
	return result
}

func (m Master) Mappings() []domain.ProviderInstrumentRef {
	return append([]domain.ProviderInstrumentRef(nil), m.mappings...)
}

func (m Master) Resolve(provider domain.Provider, token string, at time.Time) (domain.InstrumentID, error) {
	token = strings.TrimSpace(token)
	var found domain.InstrumentID
	for _, mapping := range m.mappings {
		if mapping.Provider == provider && mapping.Token == token && mapping.ValidAt(at) {
			if !found.IsZero() && found != mapping.InstrumentID {
				return domain.InstrumentID{}, ErrAmbiguousMapping
			}
			found = mapping.InstrumentID
		}
	}
	if found.IsZero() {
		return domain.InstrumentID{}, ErrInstrumentNotFound
	}
	return found, nil
}

// ResolveInstrument maps authoritative canonical identity to provider metadata
// valid at the requested instant. Provider identity never replaces InstrumentID.
func (m Master) ResolveInstrument(provider domain.Provider, id domain.InstrumentID, at time.Time) (domain.ProviderInstrumentRef, error) {
	var found domain.ProviderInstrumentRef
	for _, mapping := range m.mappings {
		if mapping.Provider != provider || mapping.InstrumentID != id || !mapping.ValidAt(at) {
			continue
		}
		if found.Token != "" {
			return domain.ProviderInstrumentRef{}, ErrAmbiguousMapping
		}
		found = mapping
	}
	if found.Token == "" {
		return domain.ProviderInstrumentRef{}, ErrInstrumentNotFound
	}
	return found, nil
}

func intervalsOverlap(leftFrom, leftUntil, rightFrom, rightUntil time.Time) bool {
	return leftFrom.Before(rightUntil) && rightFrom.Before(leftUntil)
}
