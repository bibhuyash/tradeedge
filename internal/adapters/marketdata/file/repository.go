package file

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/storage"
	"github.com/bibhuyash/tradeedge/internal/marketdata/telemetry"
)

const schemaVersion = 2

type Repository struct {
	Root      string
	Telemetry telemetry.Recorder
}

var _ storage.RevisionRepository = Repository{}

func (r Repository) Create(ctx context.Context, draft storage.DraftManifest) (storage.DatasetWriter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var parent *storage.DatasetManifest
	if draft.ParentID != "" {
		reader, err := r.Open(ctx, draft.ParentID)
		if err != nil {
			return nil, err
		}
		value := reader.Manifest()
		_ = reader.Close()
		parent = &value
	}
	normalized, err := storage.NormalizeDraft(draft, parent)
	if err != nil {
		return nil, err
	}
	draft = normalized
	if err := os.MkdirAll(r.Root, 0o750); err != nil {
		return nil, err
	}
	tempDir, err := os.MkdirTemp(r.Root, ".building-")
	if err != nil {
		return nil, err
	}
	cleanup := func(err error) (storage.DatasetWriter, error) {
		_ = os.RemoveAll(tempDir)
		return nil, err
	}
	masterBytes := draft.InstrumentMaster
	if len(masterBytes) == 0 {
		masterBytes, _ = json.Marshal(struct {
			Version string `json:"version"`
		}{Version: draft.MasterVersion})
	}
	if err := os.WriteFile(filepath.Join(tempDir, "instrument-master.json"), masterBytes, 0o640); err != nil {
		return cleanup(err)
	}
	eventsFile, eventsGzip, eventsEncoder, err := createJSONGzip(filepath.Join(tempDir, "events.ndjson.gz"))
	if err != nil {
		return cleanup(err)
	}
	qualityFile, qualityGzip, qualityEncoder, err := createJSONGzip(filepath.Join(tempDir, "quality.ndjson.gz"))
	if err != nil {
		_ = eventsGzip.Close()
		_ = eventsFile.Close()
		return cleanup(err)
	}
	return &writer{
		repository: r, draft: draft, tempDir: tempDir,
		eventsFile: eventsFile, eventsGzip: eventsGzip, eventsEncoder: eventsEncoder,
		qualityFile: qualityFile, qualityGzip: qualityGzip, qualityEncoder: qualityEncoder,
	}, nil
}

func (r Repository) Open(ctx context.Context, id storage.DatasetID) (storage.DatasetReader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validDatasetID(id) {
		return nil, storage.ErrDatasetNotFound
	}
	dir := filepath.Join(r.Root, string(id))
	manifestBytes, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, storage.ErrDatasetNotFound
	}
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(manifestBytes)))
	decoder.DisallowUnknownFields()
	var manifest storage.DatasetManifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, storage.ErrDatasetCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) ||
		manifest.SchemaVersion != schemaVersion || manifest.ID != id {
		return nil, storage.ErrDatasetCorrupt
	}
	checks := []struct {
		name string
		want string
	}{
		{"events.ndjson.gz", manifest.EventsSHA256},
		{"quality.ndjson.gz", manifest.QualitySHA256},
		{"instrument-master.json", manifest.MasterSHA256},
	}
	for _, check := range checks {
		got, err := fileSHA256(filepath.Join(dir, check.name))
		if err != nil || got != check.want {
			r.recorder().ChecksumFailure()
			return nil, storage.ErrDatasetCorrupt
		}
	}
	if computeDatasetID(manifest) != id {
		r.recorder().ChecksumFailure()
		return nil, storage.ErrDatasetCorrupt
	}
	if manifest.MasterVersion == "" || manifest.CalendarVersion == "" ||
		manifest.Source == "" || manifest.OrderingVersion == "" ||
		len(manifest.SourceSHA256) != sha256.Size*2 ||
		len(manifest.BuildKey) != sha256.Size*2 || manifest.CreatedAt.IsZero() {
		return nil, storage.ErrDatasetCorrupt
	}
	if manifest.ParentID == "" {
		if manifest.Revision != 1 {
			return nil, storage.ErrDatasetCorrupt
		}
	} else {
		if !validDatasetID(manifest.ParentID) || manifest.ParentID == id ||
			manifest.Revision <= 1 || manifest.CorrectionReason == "" || manifest.RequestID == "" {
			return nil, storage.ErrDatasetCorrupt
		}
		parent, err := r.Open(ctx, manifest.ParentID)
		if err != nil {
			return nil, storage.ErrDatasetCorrupt
		}
		parentManifest := parent.Manifest()
		_ = parent.Close()
		if manifest.Revision != parentManifest.Revision+1 {
			return nil, storage.ErrDatasetCorrupt
		}
	}
	return &reader{
		manifest:  manifest,
		eventPath: filepath.Join(dir, "events.ndjson.gz"),
	}, nil
}

type writer struct {
	repository     Repository
	draft          storage.DraftManifest
	tempDir        string
	eventsFile     *os.File
	eventsGzip     *gzip.Writer
	eventsEncoder  *json.Encoder
	qualityFile    *os.File
	qualityGzip    *gzip.Writer
	qualityEncoder *json.Encoder
	last           model.Event
	eventCount     int64
	qualityCount   int64
	start          time.Time
	end            time.Time
	closed         bool
}

func (w *writer) Append(ctx context.Context, event model.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.closed {
		return storage.ErrDatasetSealed
	}
	if event == nil || (w.last != nil && model.EventLess(event, w.last)) {
		return storage.ErrEventsOutOfOrder
	}
	record, err := recordFromEvent(event)
	if err != nil {
		return err
	}
	if err := w.eventsEncoder.Encode(record); err != nil {
		return err
	}
	if w.eventCount == 0 {
		w.start = event.ExchangeTime()
	}
	w.end = event.ExchangeTime()
	w.last = event
	w.eventCount++
	return nil
}

func (w *writer) RecordQuality(ctx context.Context, record model.QualityRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.closed {
		return storage.ErrDatasetSealed
	}
	if err := w.qualityEncoder.Encode(qualityRecordFromModel(record)); err != nil {
		return err
	}
	w.qualityCount++
	return nil
}

func (w *writer) Commit(ctx context.Context) (storage.DatasetManifest, error) {
	started := time.Now()
	if err := ctx.Err(); err != nil {
		return storage.DatasetManifest{}, err
	}
	if w.closed {
		return storage.DatasetManifest{}, storage.ErrDatasetSealed
	}
	w.closed = true
	if err := w.closeStreams(); err != nil {
		_ = os.RemoveAll(w.tempDir)
		return storage.DatasetManifest{}, err
	}
	eventsHash, err := fileSHA256(filepath.Join(w.tempDir, "events.ndjson.gz"))
	if err != nil {
		return storage.DatasetManifest{}, err
	}
	qualityHash, err := fileSHA256(filepath.Join(w.tempDir, "quality.ndjson.gz"))
	if err != nil {
		return storage.DatasetManifest{}, err
	}
	masterHash, err := fileSHA256(filepath.Join(w.tempDir, "instrument-master.json"))
	if err != nil {
		return storage.DatasetManifest{}, err
	}
	manifest := storage.DatasetManifest{
		SchemaVersion: schemaVersion, ParentID: w.draft.ParentID,
		Revision: w.draft.Revision, MasterVersion: w.draft.MasterVersion,
		CalendarVersion: w.draft.CalendarVersion, Source: w.draft.Source,
		SourceSHA256: w.draft.SourceSHA256, OrderingVersion: w.draft.OrderingVersion,
		BuildKey: w.draft.BuildKey, CorrectionReason: w.draft.CorrectionReason,
		RequestID: w.draft.RequestID, Series: w.draft.Series,
		CreatedAt: w.draft.CreatedAt.UTC(),
		Start:     w.start, End: w.end, EventCount: w.eventCount, QualityCount: w.qualityCount,
		EventsSHA256: eventsHash, QualitySHA256: qualityHash, MasterSHA256: masterHash,
	}
	manifest.ID = computeDatasetID(manifest)
	id := manifest.ID
	if existing, found, findErr := w.repository.findByBuildKey(context.Background(), manifest.BuildKey); findErr != nil {
		_ = os.RemoveAll(w.tempDir)
		return storage.DatasetManifest{}, findErr
	} else if found {
		_ = os.RemoveAll(w.tempDir)
		if existing.EventsSHA256 == manifest.EventsSHA256 &&
			existing.QualitySHA256 == manifest.QualitySHA256 &&
			existing.MasterSHA256 == manifest.MasterSHA256 {
			w.repository.recorder().DatasetCommit("idempotent", time.Since(started), 0)
			return existing, nil
		}
		return storage.DatasetManifest{}, storage.ErrDatasetCorrupt
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return storage.DatasetManifest{}, err
	}
	manifestBytes = append(manifestBytes, '\n')
	if err := os.WriteFile(filepath.Join(w.tempDir, "manifest.json"), manifestBytes, 0o640); err != nil {
		return storage.DatasetManifest{}, err
	}
	finalDir := filepath.Join(w.repository.Root, string(id))
	if _, err := os.Stat(finalDir); err == nil {
		_ = os.RemoveAll(w.tempDir)
		existing, openErr := w.repository.Open(context.Background(), id)
		if openErr != nil {
			return storage.DatasetManifest{}, storage.ErrDatasetCorrupt
		}
		existingManifest := existing.Manifest()
		_ = existing.Close()
		if existingManifest.BuildKey == manifest.BuildKey &&
			existingManifest.EventsSHA256 == manifest.EventsSHA256 &&
			existingManifest.QualitySHA256 == manifest.QualitySHA256 &&
			existingManifest.MasterSHA256 == manifest.MasterSHA256 {
			w.repository.recorder().DatasetCommit("idempotent", time.Since(started), 0)
			return existingManifest, nil
		}
		return storage.DatasetManifest{}, storage.ErrDatasetCorrupt
	} else if !errors.Is(err, os.ErrNotExist) {
		return storage.DatasetManifest{}, err
	}
	if err := os.Rename(w.tempDir, finalDir); err != nil {
		return storage.DatasetManifest{}, err
	}
	w.repository.recorder().DatasetCommit("committed", time.Since(started), datasetBytes(finalDir))
	return manifest, nil
}

func (r Repository) recorder() telemetry.Recorder {
	if r.Telemetry == nil {
		return telemetry.NopRecorder{}
	}
	return r.Telemetry
}

func datasetBytes(directory string) int64 {
	var total int64
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
	}
	return total
}

func computeDatasetID(manifest storage.DatasetManifest) storage.DatasetID {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"v2", string(manifest.ParentID), fmt.Sprintf("%d", manifest.Revision),
		manifest.MasterVersion, manifest.CalendarVersion, manifest.SourceSHA256,
		manifest.OrderingVersion, manifest.BuildKey, manifest.EventsSHA256,
		manifest.QualitySHA256, manifest.MasterSHA256,
	}, "|")))
	return storage.DatasetID(hex.EncodeToString(digest[:]))
}

func (r Repository) findByBuildKey(
	ctx context.Context,
	buildKey string,
) (storage.DatasetManifest, bool, error) {
	entries, err := os.ReadDir(r.Root)
	if errors.Is(err, os.ErrNotExist) {
		return storage.DatasetManifest{}, false, nil
	}
	if err != nil {
		return storage.DatasetManifest{}, false, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return storage.DatasetManifest{}, false, err
		}
		id := storage.DatasetID(entry.Name())
		if !entry.IsDir() || !validDatasetID(id) {
			continue
		}
		reader, err := r.Open(ctx, id)
		if err != nil {
			return storage.DatasetManifest{}, false, err
		}
		manifest := reader.Manifest()
		_ = reader.Close()
		if manifest.BuildKey == buildKey {
			return manifest, true, nil
		}
	}
	return storage.DatasetManifest{}, false, nil
}

func (w *writer) Abort(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.closed {
		return storage.ErrDatasetSealed
	}
	w.closed = true
	_ = w.closeStreams()
	return os.RemoveAll(w.tempDir)
}

func (w *writer) closeStreams() error {
	var first error
	for _, closeFn := range []func() error{
		w.eventsGzip.Close, w.eventsFile.Close, w.qualityGzip.Close, w.qualityFile.Close,
	} {
		if err := closeFn(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

type reader struct {
	manifest  storage.DatasetManifest
	eventPath string
	closed    bool
}

func (r *reader) Manifest() storage.DatasetManifest { return r.manifest }

func (r *reader) Scan(ctx context.Context, query storage.EventQuery, sink storage.EventSink) error {
	if r.closed {
		return storage.ErrDatasetSealed
	}
	if sink == nil {
		return storage.ErrDatasetCorrupt
	}
	input, err := os.Open(r.eventPath)
	if err != nil {
		return err
	}
	defer input.Close()
	compressed, err := gzip.NewReader(input)
	if err != nil {
		return storage.ErrDatasetCorrupt
	}
	defer compressed.Close()
	decoder := json.NewDecoder(compressed)
	decoder.DisallowUnknownFields()
	var previous model.Event
	var count int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var record eventRecord
		err := decoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return storage.ErrDatasetCorrupt
		}
		event, err := record.event()
		if err != nil || (previous != nil && model.EventLess(event, previous)) {
			return storage.ErrDatasetCorrupt
		}
		previous = event
		count++
		if query.Includes(event) {
			if err := sink(ctx, event); err != nil {
				return err
			}
		}
	}
	if count != r.manifest.EventCount {
		return storage.ErrDatasetCorrupt
	}
	return nil
}

func (r *reader) Close() error {
	r.closed = true
	return nil
}

func createJSONGzip(path string) (*os.File, *gzip.Writer, *json.Encoder, error) {
	output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, nil, nil, err
	}
	compressed, err := gzip.NewWriterLevel(output, gzip.BestSpeed)
	if err != nil {
		_ = output.Close()
		return nil, nil, nil, err
	}
	compressed.Header.ModTime = time.Time{}
	compressed.Header.Name = ""
	compressed.Header.Comment = ""
	compressed.Header.OS = 255
	return output, compressed, json.NewEncoder(compressed), nil
}

func fileSHA256(path string) (string, error) {
	input, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer input.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, input); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func validDatasetID(id storage.DatasetID) bool {
	if len(id) != sha256.Size*2 || strings.ContainsAny(string(id), `/\`) {
		return false
	}
	_, err := hex.DecodeString(string(id))
	return err == nil
}
