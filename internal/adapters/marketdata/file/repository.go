package file

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/storage"
)

const schemaVersion = 1

type Repository struct {
	Root string
}

var _ storage.DatasetRepository = Repository{}

func (r Repository) Create(ctx context.Context, draft storage.DraftManifest) (storage.DatasetWriter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if draft.MasterVersion == "" || draft.Source == "" || draft.OrderingVersion == "" || draft.CreatedAt.IsZero() {
		return nil, storage.ErrDatasetCorrupt
	}
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
	var manifest storage.DatasetManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil ||
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
	idDigest := sha256.Sum256([]byte(w.draft.MasterVersion + "|" + eventsHash + "|" + qualityHash + "|" + masterHash))
	id := storage.DatasetID(hex.EncodeToString(idDigest[:]))
	manifest := storage.DatasetManifest{
		SchemaVersion: schemaVersion, ID: id, ParentID: w.draft.ParentID,
		MasterVersion: w.draft.MasterVersion, Source: w.draft.Source,
		OrderingVersion: w.draft.OrderingVersion, CreatedAt: w.draft.CreatedAt.UTC(),
		Start: w.start, End: w.end, EventCount: w.eventCount, QualityCount: w.qualityCount,
		EventsSHA256: eventsHash, QualitySHA256: qualityHash, MasterSHA256: masterHash,
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
		return storage.DatasetManifest{}, storage.ErrDatasetSealed
	} else if !errors.Is(err, os.ErrNotExist) {
		return storage.DatasetManifest{}, err
	}
	if err := os.Rename(w.tempDir, finalDir); err != nil {
		return storage.DatasetManifest{}, err
	}
	return manifest, nil
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
