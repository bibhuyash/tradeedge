package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	"github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
)

const RuntimeBundleSchemaVersion = "tradeedge-runtime-bundle/v1"

type FileReference struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type RuntimeBundleManifest struct {
	SchemaVersion    string        `json:"schema_version"`
	Mode             string        `json:"mode"`
	Calendar         FileReference `json:"calendar"`
	InstrumentMaster FileReference `json:"instrument_master"`
	Watchlist        FileReference `json:"watchlist"`
	Strategies       FileReference `json:"strategies"`
	Portfolio        FileReference `json:"portfolio"`
	Risk             FileReference `json:"risk"`
}
type RuntimeBundle struct {
	Manifest     RuntimeBundleManifest
	Checksum     string
	CalendarPath string
	Master       instrumentmaster.Master
	Watchlist    readiness.Watchlist
	Tokens       []string
	Files        map[string][]byte
}

func LoadRuntimeBundle(path string) (RuntimeBundle, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return RuntimeBundle{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest RuntimeBundleManifest
	if decoder.Decode(&manifest) != nil || decoder.Decode(&struct{}{}) != io.EOF || manifest.SchemaVersion != RuntimeBundleSchemaVersion || manifest.Mode != ZerodhaModePaper {
		return RuntimeBundle{}, errors.New("invalid runtime bundle")
	}
	base := filepath.Dir(path)
	result := RuntimeBundle{Manifest: manifest, Files: map[string][]byte{}}
	references := []struct {
		name string
		ref  FileReference
	}{{"calendar", manifest.Calendar}, {"instrument_master", manifest.InstrumentMaster}, {"watchlist", manifest.Watchlist}, {"strategies", manifest.Strategies}, {"portfolio", manifest.Portfolio}, {"risk", manifest.Risk}}
	for _, item := range references {
		resolved, data, readErr := verifiedFile(base, item.ref)
		if readErr != nil {
			return RuntimeBundle{}, fmt.Errorf("load %s: %w", item.name, readErr)
		}
		result.Files[item.name] = data
		if item.name == "calendar" {
			result.CalendarPath = resolved
		}
	}
	masterPath := resolve(base, manifest.InstrumentMaster.Path)
	master, keys, err := instrumentmaster.LoadFile(masterPath)
	if err != nil {
		return RuntimeBundle{}, err
	}
	watchlist, tokens, err := decodeWatchlist(result.Files["watchlist"], master, keys)
	if err != nil {
		return RuntimeBundle{}, err
	}
	var strategies struct {
		SchemaVersion string            `json:"schema_version"`
		Instances     []json.RawMessage `json:"instances"`
	}
	if strict(result.Files["strategies"], &strategies) != nil || strategies.SchemaVersion != "market-validation-strategies/v1" || len(strategies.Instances) != 0 {
		return RuntimeBundle{}, errors.New("M1 production composition permits zero strategies only")
	}
	sum := sha256.Sum256(raw)
	result.Checksum, result.Master, result.Watchlist, result.Tokens = hex.EncodeToString(sum[:]), master, watchlist, tokens
	return result, nil
}

func verifiedFile(base string, ref FileReference) (string, []byte, error) {
	if strings.TrimSpace(ref.Path) == "" || len(ref.SHA256) != 64 || sensitivePath(ref.Path) {
		return "", nil, errors.New("invalid file reference")
	}
	resolved := resolve(base, ref.Path)
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != strings.ToLower(ref.SHA256) {
		return "", nil, errors.New("file checksum mismatch")
	}
	return resolved, raw, nil
}
func resolve(base, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(base, filepath.Clean(value))
}
func sensitivePath(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "secret") || strings.Contains(lower, "credential") || strings.Contains(lower, "token")
}
func strict(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func decodeWatchlist(raw []byte, master instrumentmaster.Master, keys map[string]domain.InstrumentID) (readiness.Watchlist, []string, error) {
	var encoded struct {
		SchemaVersion int    `json:"schema_version"`
		ID            string `json:"id"`
		Requirements  []struct {
			Provider      domain.Provider `json:"provider"`
			InstrumentKey string          `json:"instrument_key"`
			Exchange      domain.Exchange `json:"exchange"`
			Segment       domain.Segment  `json:"segment"`
			EventKind     string          `json:"event_kind"`
			Required      bool            `json:"required"`
		} `json:"requirements"`
	}
	if strict(raw, &encoded) != nil || encoded.SchemaVersion != 1 || len(encoded.Requirements) < 1 || len(encoded.Requirements) > 4 {
		return readiness.Watchlist{}, nil, errors.New("invalid watchlist")
	}
	requirements := make([]readiness.Requirement, 0, len(encoded.Requirements))
	tokens := make([]string, 0, len(encoded.Requirements))
	at := master.AsOf()
	for _, item := range encoded.Requirements {
		id := keys[item.InstrumentKey]
		instrument, found := master.Instrument(id)
		if !found || item.Provider != "zerodha" || item.EventKind != "QUOTE" || !item.Required || instrument.Exchange() != item.Exchange || instrument.Segment() != item.Segment {
			return readiness.Watchlist{}, nil, errors.New("invalid watchlist mapping")
		}
		mapping, err := master.ResolveInstrument(item.Provider, id, at)
		if err != nil {
			return readiness.Watchlist{}, nil, err
		}
		requirements = append(requirements, readiness.Requirement{Provider: item.Provider, InstrumentID: id, Exchange: item.Exchange, Segment: item.Segment, EventKind: "QUOTE", Required: true})
		tokens = append(tokens, mapping.Token)
	}
	value, err := readiness.NewWatchlist(encoded.ID, requirements)
	return value, tokens, err
}
