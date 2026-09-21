package dataset

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

// HistoricalDataSource is the provider-neutral acquisition boundary. Adapters
// terminate provider file/API representations before returning this contract.
type HistoricalDataSource interface {
	Load() (SourceDataset, error)
}

type SourceDataset struct {
	Source, SourceVersion, Market string
	DataClass                     string
	AcquiredAt                    time.Time
	RequestedStart, RequestedEnd  string
	Interval                      time.Duration
	RealData                      bool
	Instruments                   []Instrument
	Bars                          []HistoricalBar
	Calendar                      Calendar
	RawFiles                      []RawFileProvenance
}

func ImportSource(source HistoricalDataSource) (CanonicalDataset, error) {
	raw, err := source.Load()
	if err != nil {
		return CanonicalDataset{}, err
	}
	return NormalizeSource(raw)
}

func NormalizeSource(raw SourceDataset) (CanonicalDataset, error) {
	if strings.TrimSpace(raw.Source) == "" || strings.TrimSpace(raw.Market) == "" || raw.AcquiredAt.IsZero() ||
		raw.Interval <= 0 || len(raw.Instruments) == 0 || len(raw.RawFiles) < 3 {
		return CanonicalDataset{}, ErrInvalid
	}
	if raw.Interval != time.Minute && raw.Interval != 5*time.Minute && raw.Interval != 15*time.Minute {
		return CanonicalDataset{}, fmt.Errorf("%w: unsupported interval", ErrInvalid)
	}
	files := append([]RawFileProvenance(nil), raw.RawFiles...)
	sort.Slice(files, func(i, j int) bool {
		if files[i].Role != files[j].Role {
			return files[i].Role < files[j].Role
		}
		return files[i].SourceID < files[j].SourceID
	})
	for _, f := range files {
		if f.Role == "" || f.SourceID == "" || len(f.SHA256) != 64 {
			return CanonicalDataset{}, ErrInvalid
		}
		if _, err := hex.DecodeString(f.SHA256); err != nil {
			return CanonicalDataset{}, ErrInvalid
		}
	}
	fileSet := RawFileSet{}
	for _, f := range files {
		switch f.Role {
		case "bars":
			fileSet.Bars = f
		case "instruments":
			fileSet.Instruments = f
		case "calendar":
			fileSet.Calendar = f
		default:
			return CanonicalDataset{}, ErrInvalid
		}
	}
	if fileSet.Bars.Role == "" || fileSet.Instruments.Role == "" || fileSet.Calendar.Role == "" {
		return CanonicalDataset{}, ErrInvalid
	}
	rawBytes, _ := json.Marshal(files)
	rawSum := sha256.Sum256(rawBytes)
	instruments := append([]Instrument(nil), raw.Instruments...)
	sort.Slice(instruments, func(i, j int) bool { return instruments[i].ID < instruments[j].ID })
	findings := ValidateBars(instruments, raw.Bars, raw.Calendar, raw.Interval)
	findings = append(findings, requiredM3CoverageFindings(instruments, raw.Bars)...)
	bars := sortedHistoricalBars(raw.Bars)
	content := struct {
		Instruments []Instrument    `json:"instruments"`
		Bars        []HistoricalBar `json:"bars"`
	}{instruments, bars}
	contentBytes, _ := json.Marshal(content)
	normalizedSum := sha256.Sum256(contentBytes)
	realData := raw.RealData && raw.DataClass == "EXTERNAL_MARKET_DATA" && !strings.Contains(strings.ToUpper(raw.Source), "SYNTHETIC")
	semantic := v2Configuration{Source: raw.Source, SourceVersion: raw.SourceVersion, Market: raw.Market, DataClass: raw.DataClass, RequestedStart: raw.RequestedStart, RequestedEnd: raw.RequestedEnd, Interval: raw.Interval.String(), Calendar: raw.Calendar, RawChecksum: hex.EncodeToString(rawSum[:]), RealData: realData}
	semanticBytes, _ := json.Marshal(semantic)
	configSum := sha256.Sum256(semanticBytes)
	versionSum := sha256.Sum256([]byte(hex.EncodeToString(normalizedSum[:]) + "|" + hex.EncodeToString(configSum[:])))
	version := hex.EncodeToString(versionSum[:])
	report := buildBarReport(version, instruments, bars, raw.Calendar, findings)
	start, end := barBounds(bars)
	contracts := 0
	for _, i := range instruments {
		if i.Type == domain.InstrumentFuture || i.Type == domain.InstrumentOption {
			contracts++
		}
	}
	manifest := DatasetManifest{
		SchemaVersion: ManifestSchemaVersionV2, DatasetVersion: version, Source: raw.Source,
		SourceVersion: raw.SourceVersion, CalendarVersion: raw.Calendar.Version,
		Start: start, End: end, RecordCount: len(bars), ContentChecksum: hex.EncodeToString(normalizedSum[:]),
		ConfigurationChecksum: hex.EncodeToString(configSum[:]), AcquiredAt: raw.AcquiredAt.UTC(), Market: raw.Market, DataClass: raw.DataClass,
		Interval: raw.Interval.String(), RequestedStart: raw.RequestedStart, RequestedEnd: raw.RequestedEnd,
		RawChecksum: hex.EncodeToString(rawSum[:]), NormalizedChecksum: hex.EncodeToString(normalizedSum[:]),
		RawFiles: fileSet, RealData: realData, ContractCount: contracts,
	}
	calendar := raw.Calendar
	return CanonicalDataset{SchemaVersion: SchemaVersionV2, Manifest: manifest, Instruments: instruments, Bars: bars, Calendar: &calendar, Quality: report}, nil
}

func ValidateBars(instruments []Instrument, bars []HistoricalBar, calendar Calendar, interval time.Duration) []QualityFinding {
	findings := []QualityFinding{}
	byID := map[string]Instrument{}
	contractKeys := map[string]bool{}
	for _, i := range instruments {
		if _, exists := byID[i.ID]; exists {
			findings = append(findings, finding(DuplicateContract, Fatal, 0, i.ID, "", "duplicate instrument identity"))
		}
		byID[i.ID] = i
		strike := int64(0)
		if i.StrikeMinor != nil {
			strike = *i.StrikeMinor
		}
		key := fmt.Sprintf("%s|%s|%s|%s|%d", i.Underlying, i.Symbol, i.Type, i.Expiry, strike)
		if contractKeys[key] {
			findings = append(findings, finding(DuplicateContract, Fatal, 0, i.ID, "", "duplicate historical contract"))
		}
		contractKeys[key] = true
	}
	seen := map[string]bool{}
	last := map[string]time.Time{}
	for index, b := range bars {
		row := b.SourceSequence
		if row == 0 {
			row = index + 1
		}
		i, ok := byID[b.InstrumentID]
		stamp := b.EndTime.UTC().Format(time.RFC3339Nano)
		if !ok || !i.AvailableAt(b.StartTime) || !i.AvailableAt(b.EndTime) {
			findings = append(findings, finding(UnknownInstrument, Fatal, row, b.InstrumentID, stamp, "instrument metadata unavailable at bar close"))
			continue
		}
		if !b.EndTime.After(b.StartTime) || b.EndTime.Sub(b.StartTime) != interval || b.StartTime.Nanosecond() != 0 || b.StartTime.Second() != 0 || b.StartTime.Unix()%int64(interval/time.Second) != 0 {
			findings = append(findings, finding(MisalignedInterval, Error, row, b.InstrumentID, stamp, "bar is not aligned to the declared interval"))
		}
		if b.OpenMinor < 0 || b.HighMinor < 0 || b.LowMinor < 0 || b.CloseMinor < 0 || b.LowMinor > b.OpenMinor || b.LowMinor > b.CloseMinor || b.HighMinor < b.OpenMinor || b.HighMinor < b.CloseMinor || b.LowMinor > b.HighMinor {
			findings = append(findings, finding(InvalidOHLC, Error, row, b.InstrumentID, stamp, "OHLC values are inconsistent"))
		}
		if b.Volume != nil && *b.Volume < 0 {
			findings = append(findings, finding(InvalidVolume, Error, row, b.InstrumentID, stamp, "negative volume"))
		}
		if b.OpenInterest != nil && *b.OpenInterest < 0 {
			findings = append(findings, finding(InvalidOpenInterest, Error, row, b.InstrumentID, stamp, "negative open interest"))
		}
		if previous, exists := last[b.InstrumentID]; exists && b.StartTime.Before(previous) {
			findings = append(findings, finding(OutOfOrderEvent, Warning, row, b.InstrumentID, stamp, "source order moved backward"))
		}
		last[b.InstrumentID] = b.StartTime
		key := b.InstrumentID + "|" + b.StartTime.UTC().Format(time.RFC3339Nano)
		if seen[key] {
			findings = append(findings, finding(DuplicateEvent, Error, row, b.InstrumentID, stamp, "duplicate instrument interval"))
		}
		seen[key] = true
		_, startsInside := calendar.sessionAt(b.StartTime)
		_, endsInside := calendar.sessionAt(b.EndTime.Add(-time.Nanosecond))
		if !startsInside || !endsInside {
			findings = append(findings, finding(OutsideSession, Error, row, b.InstrumentID, stamp, "bar starts outside an explicit trading session"))
		}
		if i.Expiry != "" && b.EndTime.In(mustLocation(calendar.Timezone)).Format("2006-01-02") > i.Expiry {
			findings = append(findings, finding(ExpiryInconsistency, Error, row, b.InstrumentID, stamp, "bar closes after contract expiry"))
		}
	}
	findings = append(findings, barCoverageFindings(bars, calendar, interval)...)
	return findings
}

func requiredM3CoverageFindings(instruments []Instrument, bars []HistoricalBar) []QualityFinding {
	byID := map[string]Instrument{}
	for _, i := range instruments {
		byID[i.ID] = i
	}
	present := map[string]bool{}
	for _, b := range bars {
		if i, ok := byID[b.InstrumentID]; ok {
			present[i.Underlying+"|"+string(i.Type)] = true
		}
	}
	out := []QualityFinding{}
	for _, u := range []string{"NIFTY", "BANKNIFTY"} {
		for _, kind := range []domain.InstrumentType{domain.InstrumentIndex, domain.InstrumentFuture} {
			key := u + "|" + string(kind)
			if !present[key] {
				out = append(out, finding(MissingRequiredReference, Error, 0, "", "", fmt.Sprintf("%s %s bars absent", u, kind)))
			}
		}
	}
	return out
}

func barCoverageFindings(bars []HistoricalBar, c Calendar, interval time.Duration) []QualityFinding {
	present := map[string]map[int64]bool{}
	type span struct{ first, last string }
	spans := map[string]span{}
	for _, b := range bars {
		d, inside := c.sessionAt(b.StartTime)
		if !inside {
			continue
		}
		current := spans[b.InstrumentID]
		if current.first == "" || d.Date < current.first {
			current.first = d.Date
		}
		if current.last == "" || d.Date > current.last {
			current.last = d.Date
		}
		spans[b.InstrumentID] = current
		for _, s := range d.Sessions {
			a, z, _ := sessionTimes(c, d, s)
			if !b.StartTime.Before(a) && b.StartTime.Before(z) {
				key := b.InstrumentID + "|" + d.Date
				if present[key] == nil {
					present[key] = map[int64]bool{}
				}
				present[key][b.StartTime.Sub(a).Nanoseconds()/interval.Nanoseconds()] = true
			}
		}
	}
	out := []QualityFinding{}
	for id, span := range spans {
		for _, d := range c.Days {
			if !d.Trading || d.Date < span.first || d.Date > span.last {
				continue
			}
			key := id + "|" + d.Date
			if _, ok := present[key]; ok {
				continue
			}
			expected := 0
			for _, s := range d.Sessions {
				a, z, _ := sessionTimes(c, d, s)
				expected += int(z.Sub(a) / interval)
			}
			out = append(out, finding(MissingInterval, Warning, 0, id, d.Date, fmt.Sprintf("missing %d of %d expected intervals", expected, expected)))
		}
	}
	for key, buckets := range present {
		parts := strings.Split(key, "|")
		day := parts[1]
		expected := 0
		for _, d := range c.Days {
			if d.Date == day {
				for _, s := range d.Sessions {
					a, z, _ := sessionTimes(c, d, s)
					expected += int(z.Sub(a) / interval)
				}
			}
		}
		if len(buckets) < expected {
			out = append(out, finding(MissingInterval, Warning, 0, parts[0], day, fmt.Sprintf("missing %d of %d expected intervals", expected-len(buckets), expected)))
		}
	}
	return out
}

func buildBarReport(version string, instruments []Instrument, bars []HistoricalBar, c Calendar, findings []QualityFinding) DatasetQualityReport {
	r := DatasetQualityReport{DatasetVersion: version, InstrumentCount: len(instruments), ObservationCount: len(bars), QualityFindings: findings, QualificationState: ResearchReady}
	start, end := barBounds(bars)
	if !start.IsZero() {
		r.StartDate = start.In(mustLocation(c.Timezone)).Format("2006-01-02")
		r.EndDate = end.In(mustLocation(c.Timezone)).Format("2006-01-02")
	}
	days := map[string]bool{}
	for _, b := range bars {
		days[b.StartTime.In(mustLocation(c.Timezone)).Format("2006-01-02")] = true
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
	for _, f := range findings {
		switch f.Code {
		case MissingInterval:
			r.MissingIntervals++
			var missing int
			if _, err := fmt.Sscanf(f.Detail, "missing %d of", &missing); err == nil {
				r.MissingBars += missing
			}
		case DuplicateEvent:
			r.DuplicateEvents++
		case OutOfOrderEvent:
			r.OutOfOrderEvents++
		case UnknownInstrument:
			r.UnknownInstruments++
		}
		if f.Severity == Error || f.Severity == Fatal {
			r.InvalidRecords++
			r.QualificationState = Rejected
		} else if f.Severity == Warning && r.QualificationState == ResearchReady {
			r.QualificationState = Degraded
		}
	}
	return r
}

func sortedHistoricalBars(in []HistoricalBar) []HistoricalBar {
	out := append([]HistoricalBar(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartTime.Equal(out[j].StartTime) {
			return out[i].StartTime.Before(out[j].StartTime)
		}
		return out[i].InstrumentID < out[j].InstrumentID
	})
	return out
}
func barBounds(b []HistoricalBar) (time.Time, time.Time) {
	if len(b) == 0 {
		return time.Time{}, time.Time{}
	}
	s, e := b[0].StartTime, b[0].EndTime
	for _, x := range b[1:] {
		if x.StartTime.Before(s) {
			s = x.StartTime
		}
		if x.EndTime.After(e) {
			e = x.EndTime
		}
	}
	return s.UTC(), e.UTC()
}

func validateV2Checksum(d CanonicalDataset) error {
	content := struct {
		Instruments []Instrument    `json:"instruments"`
		Bars        []HistoricalBar `json:"bars"`
	}{d.Instruments, sortedHistoricalBars(d.Bars)}
	raw, err := json.Marshal(content)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	normalized := hex.EncodeToString(sum[:])
	if normalized != d.Manifest.NormalizedChecksum || normalized != d.Manifest.ContentChecksum || d.Quality.DatasetVersion != d.Manifest.DatasetVersion || d.Calendar == nil {
		return ErrInvalid
	}
	files := []RawFileProvenance{d.Manifest.RawFiles.Bars, d.Manifest.RawFiles.Instruments, d.Manifest.RawFiles.Calendar}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Role != files[j].Role {
			return files[i].Role < files[j].Role
		}
		return files[i].SourceID < files[j].SourceID
	})
	fileRaw, _ := json.Marshal(files)
	rawSum := sha256.Sum256(fileRaw)
	rawChecksum := hex.EncodeToString(rawSum[:])
	if rawChecksum != d.Manifest.RawChecksum {
		return ErrInvalid
	}
	semantic := v2Configuration{Source: d.Manifest.Source, SourceVersion: d.Manifest.SourceVersion, Market: d.Manifest.Market, DataClass: d.Manifest.DataClass, RequestedStart: d.Manifest.RequestedStart, RequestedEnd: d.Manifest.RequestedEnd, Interval: d.Manifest.Interval, Calendar: *d.Calendar, RawChecksum: rawChecksum, RealData: d.Manifest.RealData}
	semanticRaw, _ := json.Marshal(semantic)
	configSum := sha256.Sum256(semanticRaw)
	if hex.EncodeToString(configSum[:]) != d.Manifest.ConfigurationChecksum {
		return ErrInvalid
	}
	version := sha256.Sum256([]byte(normalized + "|" + d.Manifest.ConfigurationChecksum))
	if hex.EncodeToString(version[:]) != d.Manifest.DatasetVersion {
		return ErrInvalid
	}
	return nil
}

type v2Configuration struct {
	Source, SourceVersion, Market string
	DataClass                     string
	RequestedStart, RequestedEnd  string
	Interval                      string
	Calendar                      Calendar
	RawChecksum                   string
	RealData                      bool
}

// Revalidate verifies immutable content and derives quality from canonical
// bars and the embedded versioned calendar.
func Revalidate(d CanonicalDataset) (DatasetQualityReport, error) {
	if err := ValidateArtifact(d); err != nil {
		return DatasetQualityReport{}, err
	}
	if d.SchemaVersion == SchemaVersion {
		report := d.Quality
		if report.QualificationState == Qualified {
			report.QualificationState = ResearchReady
		}
		return report, nil
	}
	if d.Calendar == nil {
		return DatasetQualityReport{}, ErrInvalid
	}
	interval, err := time.ParseDuration(d.Manifest.Interval)
	if err != nil {
		return DatasetQualityReport{}, ErrInvalid
	}
	findings := ValidateBars(d.Instruments, d.Bars, *d.Calendar, interval)
	findings = append(findings, requiredM3CoverageFindings(d.Instruments, d.Bars)...)
	return buildBarReport(d.Manifest.DatasetVersion, d.Instruments, d.Bars, *d.Calendar, findings), nil
}

// CompletedObservations exposes bar closes at EndTime, never at StartTime.
// Bid and ask remain absent because an OHLC source cannot establish a book.
func (d CanonicalDataset) CompletedObservations(at time.Time) []Observation {
	out := []Observation{}
	for _, b := range d.Bars {
		if !at.Before(b.EndTime) {
			out = append(out, Observation{SourceSequence: b.SourceSequence, InstrumentID: b.InstrumentID, ExchangeTimestamp: b.EndTime, LTPMinor: b.CloseMinor, Volume: b.Volume, OpenInterest: b.OpenInterest})
		}
	}
	return sortedObservations(out)
}
