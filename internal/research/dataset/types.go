// Package dataset imports and qualifies immutable, provider-neutral historical
// market data for offline research. It has no runtime or broker dependency.
package dataset

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/research/model"
)

const (
	SchemaVersion           = "tradeedge.research.canonical-dataset/v1"
	ManifestSchemaVersion   = "tradeedge.research.dataset-manifest/v1"
	SchemaVersionV2         = "tradeedge.research.canonical-dataset/v2"
	ManifestSchemaVersionV2 = "tradeedge.research.dataset-manifest/v2"
)

var ErrInvalid = errors.New("invalid research dataset input")

type Severity string

const (
	Info    Severity = "INFO"
	Warning Severity = "WARNING"
	Error   Severity = "ERROR"
	Fatal   Severity = "FATAL"
)

type FindingCode string

const (
	DuplicateEvent               FindingCode = "DUPLICATE_EVENT"
	OutOfOrderEvent              FindingCode = "OUT_OF_ORDER_EVENT"
	InvalidTimestamp             FindingCode = "INVALID_TIMESTAMP"
	InvalidPrice                 FindingCode = "INVALID_PRICE"
	CrossedMarket                FindingCode = "CROSSED_MARKET"
	MissingInterval              FindingCode = "MISSING_INTERVAL"
	UnknownInstrument            FindingCode = "UNKNOWN_INSTRUMENT"
	InvalidOptionMetadata        FindingCode = "INVALID_OPTION_METADATA"
	PostExpiryObservation        FindingCode = "POST_EXPIRY_OBSERVATION"
	OutsideSession               FindingCode = "OUTSIDE_SESSION"
	MissingRequiredReference     FindingCode = "MISSING_REQUIRED_REFERENCE"
	NonMonotonicCumulativeVolume FindingCode = "NON_MONOTONIC_CUMULATIVE_VOLUME"
	NonMonotonicOI               FindingCode = "NON_MONOTONIC_OI"
	InvalidOHLC                  FindingCode = "INVALID_OHLC"
	InvalidVolume                FindingCode = "INVALID_VOLUME"
	InvalidOpenInterest          FindingCode = "INVALID_OPEN_INTEREST"
	DuplicateContract            FindingCode = "DUPLICATE_CONTRACT"
	ExpiryInconsistency          FindingCode = "EXPIRY_INCONSISTENCY"
	MisalignedInterval           FindingCode = "MISALIGNED_INTERVAL"
)

type QualityFinding struct {
	Code         FindingCode `json:"code"`
	Severity     Severity    `json:"severity"`
	Record       int         `json:"record,omitempty"`
	InstrumentID string      `json:"instrument_id,omitempty"`
	Timestamp    string      `json:"timestamp,omitempty"`
	Detail       string      `json:"detail"`
}

type Instrument struct {
	ID             string                `json:"instrument_id"`
	Symbol         string                `json:"symbol"`
	Underlying     string                `json:"underlying"`
	Exchange       domain.Exchange       `json:"exchange"`
	Segment        domain.Segment        `json:"segment"`
	Type           domain.InstrumentType `json:"instrument_type"`
	Expiry         string                `json:"expiry,omitempty"`
	StrikeMinor    *int64                `json:"strike_minor,omitempty"`
	OptionType     string                `json:"option_type,omitempty"`
	LotSize        *int64                `json:"lot_size,omitempty"`
	TickSizeMinor  int64                 `json:"tick_size_minor"`
	Currency       string                `json:"currency"`
	AvailableFrom  time.Time             `json:"available_from"`
	AvailableUntil *time.Time            `json:"available_until,omitempty"`
}

func (i Instrument) AvailableAt(at time.Time) bool {
	return !at.Before(i.AvailableFrom) && (i.AvailableUntil == nil || at.Before(*i.AvailableUntil))
}

type Observation struct {
	SourceSequence    int               `json:"source_sequence"`
	InstrumentID      string            `json:"instrument_id"`
	ExchangeTimestamp time.Time         `json:"exchange_timestamp"`
	LTPMinor          int64             `json:"ltp_minor"`
	BidMinor          *int64            `json:"bid_minor,omitempty"`
	AskMinor          *int64            `json:"ask_minor,omitempty"`
	Volume            *int64            `json:"volume,omitempty"`
	OpenInterest      *int64            `json:"open_interest,omitempty"`
	Raw               map[string]string `json:"raw,omitempty"`
}

// HistoricalBar is a completed source bar. Its values become observable only
// at EndTime; StartTime is never treated as the information timestamp.
type HistoricalBar struct {
	SourceSequence int       `json:"source_sequence"`
	InstrumentID   string    `json:"instrument_id"`
	StartTime      time.Time `json:"start_time"`
	EndTime        time.Time `json:"end_time"`
	OpenMinor      int64     `json:"open_minor"`
	HighMinor      int64     `json:"high_minor"`
	LowMinor       int64     `json:"low_minor"`
	CloseMinor     int64     `json:"close_minor"`
	Volume         *int64    `json:"volume,omitempty"`
	OpenInterest   *int64    `json:"open_interest,omitempty"`
}

func (o Observation) identity() string {
	h := sha256.Sum256([]byte(o.InstrumentID + "|" + o.ExchangeTimestamp.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(h[:])
}

type QualificationState string

const (
	Qualified     QualificationState = "QUALIFIED"
	ResearchReady QualificationState = "RESEARCH_READY"
	Degraded      QualificationState = "DEGRADED"
	Rejected      QualificationState = "REJECTED"
)

type Coverage struct {
	Expected int `json:"expected"`
	Present  int `json:"present"`
	Missing  int `json:"missing"`
}
type DatasetQualityReport struct {
	DatasetVersion      string             `json:"dataset_version"`
	StartDate           string             `json:"start_date"`
	EndDate             string             `json:"end_date"`
	TradingDaysExpected int                `json:"trading_days_expected"`
	TradingDaysPresent  int                `json:"trading_days_present"`
	TradingDaysMissing  int                `json:"trading_days_missing"`
	InstrumentCount     int                `json:"instrument_count"`
	ObservationCount    int                `json:"observation_count"`
	NiftyCoverage       Coverage           `json:"nifty_coverage"`
	BankNiftyCoverage   Coverage           `json:"banknifty_coverage"`
	SpotCoverage        Coverage           `json:"spot_coverage"`
	FuturesCoverage     Coverage           `json:"futures_coverage"`
	OptionsCoverage     Coverage           `json:"options_coverage"`
	MissingIntervals    int                `json:"missing_intervals"`
	MissingBars         int                `json:"missing_bars,omitempty"`
	DuplicateEvents     int                `json:"duplicate_events"`
	OutOfOrderEvents    int                `json:"out_of_order_events"`
	InvalidRecords      int                `json:"invalid_records"`
	UnknownInstruments  int                `json:"unknown_instruments"`
	OptionExpiryCount   int                `json:"option_expiry_count"`
	OptionContractCount int                `json:"option_contract_count"`
	QualityFindings     []QualityFinding   `json:"quality_findings"`
	QualificationState  QualificationState `json:"qualification_state"`
}

type DatasetManifest struct {
	SchemaVersion           string     `json:"schema_version"`
	DatasetVersion          string     `json:"dataset_version"`
	Source                  string     `json:"source"`
	SourceVersion           string     `json:"source_version,omitempty"`
	CalendarVersion         string     `json:"calendar_version"`
	InstrumentMasterVersion string     `json:"instrument_master_version"`
	Start                   time.Time  `json:"start"`
	End                     time.Time  `json:"end"`
	RecordCount             int        `json:"record_count"`
	ContentChecksum         string     `json:"content_checksum"`
	ConfigurationChecksum   string     `json:"configuration_checksum"`
	AcquiredAt              time.Time  `json:"acquired_at,omitempty"`
	Market                  string     `json:"market,omitempty"`
	DataClass               string     `json:"data_class,omitempty"`
	Interval                string     `json:"interval,omitempty"`
	RequestedStart          string     `json:"requested_start,omitempty"`
	RequestedEnd            string     `json:"requested_end,omitempty"`
	RawChecksum             string     `json:"raw_checksum,omitempty"`
	NormalizedChecksum      string     `json:"normalized_checksum,omitempty"`
	RawFiles                RawFileSet `json:"raw_files,omitempty"`
	RealData                bool       `json:"real_data"`
	ContractCount           int        `json:"contract_count,omitempty"`
}

type RawFileProvenance struct {
	Role     string `json:"role"`
	SourceID string `json:"source_id"`
	SHA256   string `json:"sha256"`
}

type RawFileSet struct {
	Bars        RawFileProvenance `json:"bars"`
	Instruments RawFileProvenance `json:"instruments"`
	Calendar    RawFileProvenance `json:"calendar"`
}

type CanonicalDataset struct {
	SchemaVersion string               `json:"schema_version"`
	Manifest      DatasetManifest      `json:"manifest"`
	Instruments   []Instrument         `json:"instruments"`
	Observations  []Observation        `json:"observations"`
	Bars          []HistoricalBar      `json:"bars,omitempty"`
	Calendar      *Calendar            `json:"calendar,omitempty"`
	Quality       DatasetQualityReport `json:"quality"`
}

// HistoricalSource converts a qualified canonical artifact into the M1 model.
// Invalid/degraded artifacts remain inspectable but cannot silently backtest.
func (d CanonicalDataset) HistoricalSource() (model.Dataset, error) {
	return d.historicalSource(false)
}

// HistoricalSourceAllowDegraded requires an explicit call site decision; a
// rejected artifact is never admitted by this production API.
func (d CanonicalDataset) HistoricalSourceAllowDegraded() (model.Dataset, error) {
	return d.historicalSource(true)
}

func (d CanonicalDataset) historicalSource(allowDegraded bool) (model.Dataset, error) {
	state := d.Quality.QualificationState
	if state == Qualified {
		state = ResearchReady
	}
	if state == Rejected || (state == Degraded && !allowDegraded) || (state != ResearchReady && state != Degraded) {
		return model.Dataset{}, ErrInvalid
	}
	byID := make(map[string]domain.InstrumentID)
	currencies := make(map[string]string)
	items := make([]model.ResearchInstrument, 0, len(d.Instruments))
	for _, encoded := range d.Instruments {
		i, err := toDomainInstrument(encoded)
		if err != nil {
			return model.Dataset{}, err
		}
		r, err := model.NewResearchInstrument(model.InstrumentSpec{Instrument: i, AvailableFrom: encoded.AvailableFrom})
		if err != nil {
			return model.Dataset{}, err
		}
		items = append(items, r)
		byID[encoded.ID] = i.ID()
		currencies[encoded.ID] = encoded.Currency
	}
	encodedObservations := append([]Observation(nil), d.Observations...)
	if len(d.Bars) > 0 {
		_, cutoff := barBounds(d.Bars)
		encodedObservations = append(encodedObservations, d.CompletedObservations(cutoff)...)
	}
	obs := make([]model.Observation, 0, len(encodedObservations))
	for _, encoded := range encodedObservations {
		// M1's Observation contract predates M2 and cannot represent missing
		// volume. Refuse the conversion rather than fabricate zero.
		if encoded.Volume == nil {
			return model.Dataset{}, ErrInvalid
		}
		id, ok := byID[encoded.InstrumentID]
		if !ok {
			return model.Dataset{}, ErrInvalid
		}
		p, _ := domain.NewPrice(encoded.LTPMinor, currencies[encoded.InstrumentID])
		bid, err := optionalPrice(encoded.BidMinor, currencies[encoded.InstrumentID])
		if err != nil {
			return model.Dataset{}, err
		}
		ask, err := optionalPrice(encoded.AskMinor, currencies[encoded.InstrumentID])
		if err != nil {
			return model.Dataset{}, err
		}
		volume := *encoded.Volume
		o, err := model.NewObservation(model.ObservationSpec{InstrumentID: id, ExchangeTime: encoded.ExchangeTimestamp, Price: p, Bid: bid, Ask: ask, Volume: volume, OpenInterest: encoded.OpenInterest})
		if err != nil {
			return model.Dataset{}, err
		}
		obs = append(obs, o)
	}
	return model.NewDataset(model.DatasetSpec{SchemaVersion: model.DatasetSchemaV1, Version: d.Manifest.DatasetVersion, Instruments: items, Observations: obs})
}

func sortedObservations(in []Observation) []Observation {
	out := append([]Observation(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ExchangeTimestamp.Equal(out[j].ExchangeTimestamp) {
			return out[i].ExchangeTimestamp.Before(out[j].ExchangeTimestamp)
		}
		return strings.Compare(out[i].InstrumentID, out[j].InstrumentID) < 0
	})
	return out
}

func optionalPrice(v *int64, currency string) (*domain.Price, error) {
	if v == nil {
		return nil, nil
	}
	p, e := domain.NewPrice(*v, currency)
	return &p, e
}

func toDomainInstrument(i Instrument) (domain.Instrument, error) {
	u, e := domain.NewUnderlyingID(i.Underlying)
	if e != nil {
		return domain.Instrument{}, e
	}
	c, e := domain.NewCurrency(i.Currency)
	if e != nil {
		return domain.Instrument{}, e
	}
	tick, e := domain.NewPrice(i.TickSizeMinor, i.Currency)
	if e != nil {
		return domain.Instrument{}, e
	}
	var lot domain.Quantity
	if i.LotSize != nil {
		lot, e = domain.NewQuantity(*i.LotSize)
		if e != nil {
			return domain.Instrument{}, e
		}
	}
	s := domain.InstrumentSpec{Exchange: i.Exchange, Segment: i.Segment, UnderlyingID: u, Type: i.Type, ExchangeSymbol: i.Symbol, LotSize: lot, TickSize: tick, Currency: c}
	if i.Type == domain.InstrumentFuture || i.Type == domain.InstrumentOption {
		t, e := time.Parse("2006-01-02", i.Expiry)
		if e != nil {
			return domain.Instrument{}, e
		}
		date, _ := domain.NewCivilDate(t.Year(), t.Month(), t.Day())
		strike := domain.Price{}
		ot := domain.OptionNone
		if i.Type == domain.InstrumentOption {
			if i.StrikeMinor == nil {
				return domain.Instrument{}, ErrInvalid
			}
			strike, e = domain.NewPrice(*i.StrikeMinor, i.Currency)
			if e != nil {
				return domain.Instrument{}, e
			}
			if i.OptionType == "CE" {
				ot = domain.OptionCall
			} else if i.OptionType == "PE" {
				ot = domain.OptionPut
			} else {
				return domain.Instrument{}, ErrInvalid
			}
		}
		s.Derivative = &domain.DerivativeSpec{Expiry: date, Strike: strike, OptionType: ot}
	}
	result, e := domain.NewInstrument(s)
	if e != nil {
		return result, e
	}
	if i.ID != "" && result.ID().String() != i.ID {
		return domain.Instrument{}, ErrInvalid
	}
	return result, nil
}
