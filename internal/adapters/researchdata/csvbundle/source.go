// Package csvbundle terminates provider-specific mapped CSV representations at
// the provider-neutral research dataset acquisition boundary.
package csvbundle

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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/research/dataset"
)

const SchemaVersion = "tradeedge.research.mapped-csv-bundle/v1"

type FileRef struct {
	Path     string `json:"path"`
	SourceID string `json:"source_id"`
	SHA256   string `json:"sha256"`
}
type Files struct {
	Bars        FileRef `json:"bars"`
	Instruments FileRef `json:"instruments"`
	Calendar    FileRef `json:"calendar"`
}
type Columns struct {
	Instrument   string `json:"instrument"`
	Timestamp    string `json:"timestamp"`
	Open         string `json:"open"`
	High         string `json:"high"`
	Low          string `json:"low"`
	Close        string `json:"close"`
	Volume       string `json:"volume,omitempty"`
	OpenInterest string `json:"open_interest,omitempty"`
}
type Manifest struct {
	SchemaVersion      string    `json:"schema_version"`
	Provider           string    `json:"provider"`
	SourceVersion      string    `json:"source_version,omitempty"`
	DataClass          string    `json:"data_class"`
	AcquiredAt         time.Time `json:"acquired_at"`
	Market             string    `json:"market"`
	RequestedStart     string    `json:"requested_start"`
	RequestedEnd       string    `json:"requested_end"`
	Interval           string    `json:"interval"`
	Timezone           string    `json:"timezone"`
	TimestampSemantics string    `json:"timestamp_semantics"`
	PriceUnit          string    `json:"price_unit"`
	Files              Files     `json:"files"`
	Columns            Columns   `json:"columns"`
}
type Source struct{ path string }

func New(path string) Source { return Source{path: path} }

func (s Source) Load() (dataset.SourceDataset, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return dataset.SourceDataset{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m Manifest
	if dec.Decode(&m) != nil {
		return dataset.SourceDataset{}, dataset.ErrInvalid
	}
	var extra any
	if !errors.Is(dec.Decode(&extra), io.EOF) {
		return dataset.SourceDataset{}, dataset.ErrInvalid
	}
	interval, err := time.ParseDuration(m.Interval)
	if err != nil {
		return dataset.SourceDataset{}, fmt.Errorf("%w: interval", dataset.ErrInvalid)
	}
	if m.SchemaVersion != SchemaVersion || m.Provider == "" || strings.Contains(strings.ToUpper(m.Provider), "SYNTHETIC") || m.DataClass != "EXTERNAL_MARKET_DATA" || m.AcquiredAt.IsZero() || m.Market == "" || m.Timezone != "Asia/Kolkata" || m.PriceUnit != "MINOR" || (m.TimestampSemantics != "START" && m.TimestampSemantics != "END") {
		return dataset.SourceDataset{}, dataset.ErrInvalid
	}
	if m.Columns.Instrument == "" || m.Columns.Timestamp == "" || m.Columns.Open == "" || m.Columns.High == "" || m.Columns.Low == "" || m.Columns.Close == "" {
		return dataset.SourceDataset{}, dataset.ErrInvalid
	}
	base := filepath.Dir(s.path)
	refs := []struct {
		role string
		ref  FileRef
	}{{"bars", m.Files.Bars}, {"instruments", m.Files.Instruments}, {"calendar", m.Files.Calendar}}
	provenance := make([]dataset.RawFileProvenance, 0, 3)
	paths := map[string]string{}
	for _, x := range refs {
		if x.ref.Path == "" || x.ref.SourceID == "" || len(x.ref.SHA256) != 64 {
			return dataset.SourceDataset{}, dataset.ErrInvalid
		}
		if filepath.IsAbs(x.ref.Path) {
			return dataset.SourceDataset{}, fmt.Errorf("%w: absolute bundle path", dataset.ErrInvalid)
		}
		p := filepath.Join(base, filepath.Clean(x.ref.Path))
		rel, relErr := filepath.Rel(base, p)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return dataset.SourceDataset{}, fmt.Errorf("%w: bundle path escapes manifest directory", dataset.ErrInvalid)
		}
		body, e := os.ReadFile(p)
		if e != nil {
			return dataset.SourceDataset{}, e
		}
		sum := sha256.Sum256(body)
		actual := hex.EncodeToString(sum[:])
		if !strings.EqualFold(actual, x.ref.SHA256) {
			return dataset.SourceDataset{}, fmt.Errorf("%w: %s checksum", dataset.ErrInvalid, x.role)
		}
		paths[x.role] = p
		provenance = append(provenance, dataset.RawFileProvenance{Role: x.role, SourceID: x.ref.SourceID, SHA256: actual})
	}
	instruments, _, err := dataset.LoadInstrumentCSV(paths["instruments"])
	if err != nil {
		return dataset.SourceDataset{}, err
	}
	keyMap, err := instrumentKeys(paths["instruments"], instruments)
	if err != nil {
		return dataset.SourceDataset{}, err
	}
	calendar, err := dataset.LoadCalendar(paths["calendar"])
	if err != nil {
		return dataset.SourceDataset{}, err
	}
	bars, err := loadBars(paths["bars"], m.Columns, keyMap, interval, m.TimestampSemantics)
	if err != nil {
		return dataset.SourceDataset{}, err
	}
	return dataset.SourceDataset{Source: m.Provider, SourceVersion: m.SourceVersion, Market: m.Market, DataClass: m.DataClass, AcquiredAt: m.AcquiredAt, RequestedStart: m.RequestedStart, RequestedEnd: m.RequestedEnd, Interval: interval, RealData: true, Instruments: instruments, Bars: bars, Calendar: calendar, RawFiles: provenance}, nil
}

func instrumentKeys(path string, instruments []dataset.Instrument) (map[string]string, error) {
	raw, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	r := csv.NewReader(bytes.NewReader(raw))
	h, e := r.Read()
	if e != nil {
		return nil, e
	}
	cols := columns(h)
	if _, ok := cols["source_instrument_id"]; !ok {
		return nil, fmt.Errorf("%w: source_instrument_id", dataset.ErrInvalid)
	}
	bySymbol := map[string]string{}
	for _, i := range instruments {
		bySymbol[i.Symbol] = i.ID
	}
	out := map[string]string{}
	for {
		row, e := r.Read()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return nil, dataset.ErrInvalid
		}
		key := get(row, cols, "source_instrument_id")
		symbol := strings.ToUpper(get(row, cols, "symbol"))
		id, ok := bySymbol[symbol]
		if key == "" || !ok || out[key] != "" {
			return nil, dataset.ErrInvalid
		}
		out[key] = id
	}
	return out, nil
}
func loadBars(path string, c Columns, keyMap map[string]string, interval time.Duration, semantics string) ([]dataset.HistoricalBar, error) {
	raw, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	r := csv.NewReader(bytes.NewReader(raw))
	h, e := r.Read()
	if e != nil {
		return nil, e
	}
	cols := columns(h)
	required := []string{c.Instrument, c.Timestamp, c.Open, c.High, c.Low, c.Close}
	for _, name := range required {
		if _, ok := cols[strings.ToLower(name)]; !ok {
			return nil, fmt.Errorf("%w: missing mapped column %s", dataset.ErrInvalid, name)
		}
	}
	out := []dataset.HistoricalBar{}
	for rowNum := 2; ; rowNum++ {
		row, e := r.Read()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return nil, fmt.Errorf("%w: malformed bars row %d", dataset.ErrInvalid, rowNum)
		}
		sourceKey := get(row, cols, c.Instrument)
		id, ok := keyMap[sourceKey]
		if !ok {
			return nil, fmt.Errorf("%w: unknown source instrument %s", dataset.ErrInvalid, sourceKey)
		}
		at, e := time.Parse(time.RFC3339Nano, get(row, cols, c.Timestamp))
		if e != nil {
			return nil, fmt.Errorf("%w: timestamp row %d", dataset.ErrInvalid, rowNum)
		}
		start, end := at.UTC(), at.UTC().Add(interval)
		if semantics == "END" {
			end = at.UTC()
			start = end.Add(-interval)
		}
		values := make([]int64, 4)
		for idx, name := range []string{c.Open, c.High, c.Low, c.Close} {
			values[idx], e = strconv.ParseInt(get(row, cols, name), 10, 64)
			if e != nil {
				return nil, fmt.Errorf("%w: price row %d", dataset.ErrInvalid, rowNum)
			}
		}
		volume, e := optional(row, cols, c.Volume)
		if e != nil {
			return nil, dataset.ErrInvalid
		}
		oi, e := optional(row, cols, c.OpenInterest)
		if e != nil {
			return nil, dataset.ErrInvalid
		}
		out = append(out, dataset.HistoricalBar{SourceSequence: rowNum - 1, InstrumentID: id, StartTime: start, EndTime: end, OpenMinor: values[0], HighMinor: values[1], LowMinor: values[2], CloseMinor: values[3], Volume: volume, OpenInterest: oi})
	}
	return out, nil
}
func columns(h []string) map[string]int {
	m := map[string]int{}
	for i, v := range h {
		m[strings.ToLower(strings.TrimSpace(v))] = i
	}
	return m
}
func get(row []string, c map[string]int, name string) string {
	i, ok := c[strings.ToLower(name)]
	if !ok || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}
func optional(row []string, c map[string]int, name string) (*int64, error) {
	if name == "" {
		return nil, nil
	}
	v := get(row, c, name)
	if v == "" {
		return nil, nil
	}
	n, e := strconv.ParseInt(v, 10, 64)
	return &n, e
}
