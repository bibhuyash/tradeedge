package dataset

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

type ImportConfig struct {
	Source             string     `json:"source"`
	SourceVersion      string     `json:"source_version,omitempty"`
	IntervalMinutes    int        `json:"interval_minutes"`
	CumulativeVolume   bool       `json:"cumulative_volume"`
	CumulativeOI       bool       `json:"cumulative_oi"`
	BlockingSeverities []Severity `json:"blocking_severities"`
}

func ImportCSV(observationPath, instrumentPath string, calendar Calendar, config ImportConfig) (CanonicalDataset, error) {
	if strings.TrimSpace(config.Source) == "" || config.IntervalMinutes <= 0 {
		return CanonicalDataset{}, ErrInvalid
	}
	instruments, masterVersion, e := loadInstruments(instrumentPath)
	if e != nil {
		return CanonicalDataset{}, e
	}
	raw, e := os.ReadFile(observationPath)
	if e != nil {
		return CanonicalDataset{}, e
	}
	reader := csv.NewReader(bytes.NewReader(raw))
	reader.ReuseRecord = false
	header, e := reader.Read()
	if e != nil {
		return CanonicalDataset{}, e
	}
	columns := columnMap(header)
	for _, required := range []string{"exchange_timestamp", "instrument_id", "ltp_minor"} {
		if _, ok := columns[required]; !ok {
			return CanonicalDataset{}, fmt.Errorf("%w: missing %s", ErrInvalid, required)
		}
	}
	byID := map[string]Instrument{}
	for _, i := range instruments {
		byID[i.ID] = i
	}
	observations := []Observation{}
	findings := []QualityFinding{}
	seen := map[string]bool{}
	lastByInstrument := map[string]time.Time{}
	lastVolume := map[string]int64{}
	lastOI := map[string]int64{}
	for row := 2; ; row++ {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			findings = append(findings, finding(InvalidTimestamp, Error, row, "", "", "malformed CSV record"))
			continue
		}
		get := func(name string) string {
			index, ok := columns[name]
			if !ok || index >= len(record) {
				return ""
			}
			return strings.TrimSpace(record[index])
		}
		id := get("instrument_id")
		at, e := time.Parse(time.RFC3339Nano, get("exchange_timestamp"))
		if e != nil {
			findings = append(findings, finding(InvalidTimestamp, Error, row, id, get("exchange_timestamp"), "timestamp must be RFC3339"))
			continue
		}
		inst, known := byID[id]
		if !known || !inst.AvailableAt(at) {
			findings = append(findings, finding(UnknownInstrument, Fatal, row, id, at.Format(time.RFC3339Nano), "instrument metadata unavailable at event time"))
			continue
		}
		ltp, e := strconv.ParseInt(get("ltp_minor"), 10, 64)
		if e != nil || ltp < 0 {
			findings = append(findings, finding(InvalidPrice, Error, row, id, at.Format(time.RFC3339Nano), "invalid last price"))
			continue
		}
		bid, bidErr := parseOptionalInt(get("bid_minor"))
		ask, askErr := parseOptionalInt(get("ask_minor"))
		if bidErr != nil || askErr != nil || (bid != nil && *bid < 0) || (ask != nil && *ask < 0) {
			findings = append(findings, finding(InvalidPrice, Error, row, id, at.Format(time.RFC3339Nano), "invalid bid or ask"))
			continue
		}
		if bid != nil && ask != nil && *bid > *ask {
			findings = append(findings, finding(CrossedMarket, Error, row, id, at.Format(time.RFC3339Nano), "bid exceeds ask"))
			continue
		}
		volume, volumeErr := parseOptionalInt(get("volume"))
		oi, oiErr := parseOptionalInt(get("open_interest"))
		if volumeErr != nil || oiErr != nil || (volume != nil && *volume < 0) || (oi != nil && *oi < 0) {
			findings = append(findings, finding(InvalidPrice, Error, row, id, at.Format(time.RFC3339Nano), "invalid volume or open interest"))
			continue
		}
		if previous, ok := lastByInstrument[id]; ok && at.Before(previous) {
			findings = append(findings, finding(OutOfOrderEvent, Warning, row, id, at.Format(time.RFC3339Nano), "source order moved backward"))
		}
		lastByInstrument[id] = at
		key := id + "|" + at.UTC().Format(time.RFC3339Nano)
		if seen[key] {
			findings = append(findings, finding(DuplicateEvent, Error, row, id, at.Format(time.RFC3339Nano), "duplicate instrument timestamp"))
			continue
		}
		seen[key] = true
		if _, inside := calendar.sessionAt(at); !inside {
			findings = append(findings, finding(OutsideSession, Error, row, id, at.Format(time.RFC3339Nano), "observation outside an explicit trading session"))
		}
		if inst.Expiry != "" && at.In(mustLocation(calendar.Timezone)).Format("2006-01-02") > inst.Expiry {
			findings = append(findings, finding(PostExpiryObservation, Error, row, id, at.Format(time.RFC3339Nano), "observation after contract expiry"))
		}
		if config.CumulativeVolume && volume != nil {
			if prior, ok := lastVolume[id]; ok && *volume < prior {
				findings = append(findings, finding(NonMonotonicCumulativeVolume, Error, row, id, at.Format(time.RFC3339Nano), "cumulative volume decreased"))
			}
			lastVolume[id] = *volume
		}
		if config.CumulativeOI && oi != nil {
			if prior, ok := lastOI[id]; ok && *oi < prior {
				findings = append(findings, finding(NonMonotonicOI, Error, row, id, at.Format(time.RFC3339Nano), "documented cumulative OI decreased"))
			}
			lastOI[id] = *oi
		}
		rawFields := map[string]string{}
		for name, index := range columns {
			if index < len(record) && !knownCanonicalColumn(name) {
				rawFields[name] = record[index]
			}
		}
		if len(rawFields) == 0 {
			rawFields = nil
		}
		observations = append(observations, Observation{row - 1, id, at.UTC(), ltp, bid, ask, volume, oi, rawFields})
	}
	findings = append(findings, coverageFindings(observations, instruments, calendar, time.Duration(config.IntervalMinutes)*time.Minute)...)
	content := struct {
		Instruments  []Instrument  `json:"instruments"`
		Observations []Observation `json:"observations"`
	}{instruments, sortedObservations(observations)}
	contentRaw, _ := json.Marshal(content)
	contentHash := sha256.Sum256(contentRaw)
	configRaw, _ := json.Marshal(struct {
		Config        ImportConfig `json:"config"`
		Calendar      Calendar     `json:"calendar"`
		MasterVersion string       `json:"master_version"`
	}{config, calendar, masterVersion})
	configHash := sha256.Sum256(configRaw)
	versionHash := sha256.Sum256([]byte(hex.EncodeToString(contentHash[:]) + "|" + hex.EncodeToString(configHash[:])))
	start, end := bounds(observations)
	manifest := DatasetManifest{SchemaVersion: ManifestSchemaVersion, DatasetVersion: hex.EncodeToString(versionHash[:]), Source: config.Source, SourceVersion: config.SourceVersion, CalendarVersion: calendar.Version, InstrumentMasterVersion: masterVersion, Start: start, End: end, RecordCount: len(observations), ContentChecksum: hex.EncodeToString(contentHash[:]), ConfigurationChecksum: hex.EncodeToString(configHash[:])}
	report := buildReport(manifest.DatasetVersion, instruments, observations, calendar, findings, config.BlockingSeverities)
	return CanonicalDataset{SchemaVersion: SchemaVersion, Manifest: manifest, Instruments: instruments, Observations: sortedObservations(observations), Quality: report}, nil
}

func loadInstruments(path string) ([]Instrument, string, error) {
	raw, e := os.ReadFile(path)
	if e != nil {
		return nil, "", e
	}
	r := csv.NewReader(bytes.NewReader(raw))
	header, e := r.Read()
	if e != nil {
		return nil, "", e
	}
	c := columnMap(header)
	required := []string{"symbol", "underlying", "exchange", "segment", "instrument_type", "tick_size_minor", "currency", "available_from"}
	for _, v := range required {
		if _, ok := c[v]; !ok {
			return nil, "", ErrInvalid
		}
	}
	out := []Instrument{}
	for {
		row, e := r.Read()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return nil, "", ErrInvalid
		}
		get := func(n string) string {
			if x, ok := c[n]; ok && x < len(row) {
				return strings.TrimSpace(row[x])
			}
			return ""
		}
		tick, e := strconv.ParseInt(get("tick_size_minor"), 10, 64)
		if e != nil {
			return nil, "", ErrInvalid
		}
		available, e := time.Parse(time.RFC3339Nano, get("available_from"))
		if e != nil {
			return nil, "", ErrInvalid
		}
		var until *time.Time
		if v := get("available_until"); v != "" {
			x, e := time.Parse(time.RFC3339Nano, v)
			if e != nil {
				return nil, "", ErrInvalid
			}
			until = &x
		}
		var lot *int64
		if v := get("lot_size"); v != "" {
			x, e := strconv.ParseInt(v, 10, 64)
			if e != nil {
				return nil, "", ErrInvalid
			}
			lot = &x
		}
		var strike *int64
		if v := get("strike_minor"); v != "" {
			x, e := strconv.ParseInt(v, 10, 64)
			if e != nil {
				return nil, "", ErrInvalid
			}
			strike = &x
		}
		item := Instrument{Symbol: strings.ToUpper(get("symbol")), Underlying: strings.ToUpper(get("underlying")), Exchange: domain.Exchange(get("exchange")), Segment: domain.Segment(get("segment")), Type: domain.InstrumentType(get("instrument_type")), Expiry: get("expiry"), StrikeMinor: strike, OptionType: strings.ToUpper(get("option_type")), LotSize: lot, TickSizeMinor: tick, Currency: strings.ToUpper(get("currency")), AvailableFrom: available.UTC(), AvailableUntil: until}
		di, e := toDomainInstrument(item)
		if e != nil {
			return nil, "", e
		}
		item.ID = di.ID().String()
		if supplied := get("instrument_id"); supplied != "" && supplied != item.ID {
			return nil, "", ErrInvalid
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	sum := sha256.Sum256(raw)
	return out, hex.EncodeToString(sum[:]), nil
}

// LoadInstrumentCSV decodes the provider-neutral point-in-time instrument
// format used by research source adapters.
func LoadInstrumentCSV(path string) ([]Instrument, string, error) {
	return loadInstruments(path)
}

func coverageFindings(obs []Observation, instruments []Instrument, c Calendar, interval time.Duration) []QualityFinding {
	byID := map[string]Instrument{}
	for _, i := range instruments {
		byID[i.ID] = i
	}
	present := map[string]map[int64]bool{}
	for _, o := range obs {
		d, inside := c.sessionAt(o.ExchangeTimestamp)
		if !inside {
			continue
		}
		for _, s := range d.Sessions {
			a, b, _ := sessionTimes(c, d, s)
			if !o.ExchangeTimestamp.Before(a) && o.ExchangeTimestamp.Before(b) {
				key := o.InstrumentID + "|" + d.Date
				if present[key] == nil {
					present[key] = map[int64]bool{}
				}
				present[key][o.ExchangeTimestamp.Sub(a).Nanoseconds()/interval.Nanoseconds()] = true
			}
		}
	}
	out := []QualityFinding{}
	for key, buckets := range present {
		id := strings.Split(key, "|")[0]
		_ = byID[id]
		day := strings.Split(key, "|")[1]
		var cd CalendarDay
		for _, d := range c.Days {
			if d.Date == day {
				cd = d
			}
		}
		expected := 0
		for _, s := range cd.Sessions {
			a, b, _ := sessionTimes(c, cd, s)
			expected += int(b.Sub(a) / interval)
		}
		if len(buckets) < expected {
			out = append(out, finding(MissingInterval, Warning, 0, id, day, fmt.Sprintf("missing %d of %d expected intervals", expected-len(buckets), expected)))
		}
	}
	return out
}

func buildReport(version string, instruments []Instrument, obs []Observation, c Calendar, findings []QualityFinding, blocking []Severity) DatasetQualityReport {
	r := DatasetQualityReport{DatasetVersion: version, InstrumentCount: len(instruments), ObservationCount: len(obs), QualityFindings: findings}
	start, end := bounds(obs)
	if !start.IsZero() {
		r.StartDate = start.In(mustLocation(c.Timezone)).Format("2006-01-02")
		r.EndDate = end.In(mustLocation(c.Timezone)).Format("2006-01-02")
	}
	days := map[string]bool{}
	for _, o := range obs {
		days[o.ExchangeTimestamp.In(mustLocation(c.Timezone)).Format("2006-01-02")] = true
	}
	for _, d := range c.Days {
		if d.Trading && d.Date >= r.StartDate && d.Date <= r.EndDate {
			r.TradingDaysExpected++
			if days[d.Date] {
				r.TradingDaysPresent++
			} else {
				r.TradingDaysMissing++
			}
		}
	}
	r.NiftyCoverage = coverageFor(instruments, obs, r.TradingDaysExpected, func(i Instrument) bool { return i.Underlying == "NIFTY" })
	r.BankNiftyCoverage = coverageFor(instruments, obs, r.TradingDaysExpected, func(i Instrument) bool { return i.Underlying == "BANKNIFTY" })
	r.SpotCoverage = coverageFor(instruments, obs, r.TradingDaysExpected, func(i Instrument) bool { return i.Type == domain.InstrumentIndex })
	r.FuturesCoverage = coverageFor(instruments, obs, r.TradingDaysExpected, func(i Instrument) bool { return i.Type == domain.InstrumentFuture })
	r.OptionsCoverage = coverageFor(instruments, obs, r.TradingDaysExpected, func(i Instrument) bool { return i.Type == domain.InstrumentOption })
	expiries := map[string]bool{}
	hasRef := map[string]bool{}
	for _, i := range instruments {
		if i.Type == domain.InstrumentOption {
			r.OptionContractCount++
			expiries[i.Expiry] = true
		}
		if i.Type == domain.InstrumentIndex || i.Type == domain.InstrumentFuture {
			hasRef[i.Underlying] = true
		}
	}
	r.OptionExpiryCount = len(expiries)
	if !hasRef["NIFTY"] {
		r.QualityFindings = append(r.QualityFindings, finding(MissingRequiredReference, Error, 0, "", "", "NIFTY spot/future reference absent"))
	}
	if !hasRef["BANKNIFTY"] {
		r.QualityFindings = append(r.QualityFindings, finding(MissingRequiredReference, Error, 0, "", "", "BANKNIFTY spot/future reference absent"))
	}
	for _, f := range r.QualityFindings {
		switch f.Code {
		case MissingInterval:
			r.MissingIntervals++
		case DuplicateEvent:
			r.DuplicateEvents++
		case OutOfOrderEvent:
			r.OutOfOrderEvents++
		case UnknownInstrument:
			r.UnknownInstruments++
		default:
			if f.Severity == Error || f.Severity == Fatal {
				r.InvalidRecords++
			}
		}
	}
	r.QualificationState = Qualified
	blocked := map[Severity]bool{}
	if len(blocking) == 0 {
		blocked[Error] = true
		blocked[Fatal] = true
	} else {
		for _, s := range blocking {
			blocked[s] = true
		}
	}
	for _, f := range r.QualityFindings {
		if blocked[f.Severity] {
			r.QualificationState = Rejected
			break
		}
		if r.QualificationState == Qualified && (f.Severity == Warning || f.Severity == Error) {
			r.QualificationState = Degraded
		}
	}
	return r
}

func coverageFor(instruments []Instrument, observations []Observation, expected int, include func(Instrument) bool) Coverage {
	eligible := map[string]bool{}
	for _, instrument := range instruments {
		if include(instrument) {
			eligible[instrument.ID] = true
		}
	}
	days := map[string]bool{}
	for _, observation := range observations {
		if eligible[observation.InstrumentID] {
			days[observation.ExchangeTimestamp.UTC().Format("2006-01-02")] = true
		}
	}
	present := len(days)
	missing := expected - present
	if missing < 0 {
		missing = 0
	}
	return Coverage{Expected: expected, Present: present, Missing: missing}
}

func Save(path string, d CanonicalDataset) error {
	raw, e := json.MarshalIndent(d, "", "  ")
	if e != nil {
		return e
	}
	raw = append(raw, '\n')
	file, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0444)
	if e != nil {
		return e
	}
	if _, e = file.Write(raw); e != nil {
		_ = file.Close()
		return e
	}
	return file.Close()
}
func Load(path string) (CanonicalDataset, error) {
	raw, e := os.ReadFile(path)
	if e != nil {
		return CanonicalDataset{}, e
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var d CanonicalDataset
	if dec.Decode(&d) != nil || (d.SchemaVersion != SchemaVersion && d.SchemaVersion != SchemaVersionV2) {
		return CanonicalDataset{}, ErrInvalid
	}
	var extra any
	if !errors.Is(dec.Decode(&extra), io.EOF) {
		return CanonicalDataset{}, ErrInvalid
	}
	return d, nil
}

// ValidateArtifact detects mutation of canonical content after import.
func ValidateArtifact(d CanonicalDataset) error {
	if d.SchemaVersion == SchemaVersionV2 {
		return validateV2Checksum(d)
	}
	content := struct {
		Instruments  []Instrument  `json:"instruments"`
		Observations []Observation `json:"observations"`
	}{d.Instruments, sortedObservations(d.Observations)}
	raw, err := json.Marshal(content)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != d.Manifest.ContentChecksum || d.Quality.DatasetVersion != d.Manifest.DatasetVersion {
		return ErrInvalid
	}
	version := sha256.Sum256([]byte(d.Manifest.ContentChecksum + "|" + d.Manifest.ConfigurationChecksum))
	if hex.EncodeToString(version[:]) != d.Manifest.DatasetVersion {
		return ErrInvalid
	}
	return nil
}

// Storage keeps physical persistence replaceable without changing semantics.
type Storage interface {
	Save(string, CanonicalDataset) error
	Load(string) (CanonicalDataset, error)
}
type LocalStorage struct{}

func (LocalStorage) Save(path string, d CanonicalDataset) error { return Save(path, d) }
func (LocalStorage) Load(path string) (CanonicalDataset, error) { return Load(path) }
func columnMap(h []string) map[string]int {
	m := map[string]int{}
	for i, v := range h {
		m[strings.ToLower(strings.TrimSpace(v))] = i
	}
	return m
}
func parseOptionalInt(v string) (*int64, error) {
	if v == "" {
		return nil, nil
	}
	n, e := strconv.ParseInt(v, 10, 64)
	return &n, e
}
func finding(c FindingCode, s Severity, row int, id, at, detail string) QualityFinding {
	return QualityFinding{c, s, row, id, at, detail}
}
func bounds(obs []Observation) (time.Time, time.Time) {
	if len(obs) == 0 {
		return time.Time{}, time.Time{}
	}
	s := obs[0].ExchangeTimestamp
	e := s
	for _, o := range obs[1:] {
		if o.ExchangeTimestamp.Before(s) {
			s = o.ExchangeTimestamp
		}
		if o.ExchangeTimestamp.After(e) {
			e = o.ExchangeTimestamp
		}
	}
	return s.UTC(), e.UTC()
}
func knownCanonicalColumn(s string) bool {
	switch s {
	case "exchange_timestamp", "instrument_id", "ltp_minor", "bid_minor", "ask_minor", "volume", "open_interest":
		return true
	}
	return false
}
func mustLocation(name string) *time.Location {
	l, e := time.LoadLocation(name)
	if e != nil {
		panic(e)
	}
	return l
}
