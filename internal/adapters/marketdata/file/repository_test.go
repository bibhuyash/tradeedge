package file

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/replay"
	"github.com/bibhuyash/tradeedge/internal/marketdata/storage"
)

func TestRepositoryRoundTripAndChecksumValidation(t *testing.T) {
	root := t.TempDir()
	repository := Repository{Root: root}
	writer, err := repository.Create(context.Background(), storage.DraftManifest{
		MasterVersion: "v1", CalendarVersion: "calendar-v1",
		InstrumentMaster: []byte("{\"version\":\"v1\"}\n"),
		Source:           "fixture", OrderingVersion: "v1", CreatedAt: time.Unix(0, 0),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	event := testQuote(t)
	if err := writer.Append(context.Background(), event); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := writer.RecordQuality(context.Background(), model.QualityRecord{
		Code: model.QualityDuplicate, Disposition: model.DispositionSuppressed,
		ObservedAt: time.Unix(1, 0), Reason: "test",
	}); err != nil {
		t.Fatalf("RecordQuality() error = %v", err)
	}
	manifest, err := writer.Commit(context.Background())
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if manifest.EventCount != 1 || manifest.MasterSHA256 == "" {
		t.Fatalf("manifest = %#v", manifest)
	}
	reader, err := repository.Open(context.Background(), manifest.ID)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	var events []model.Event
	if err := reader.Scan(context.Background(), storage.EventQuery{}, func(_ context.Context, got model.Event) error {
		events = append(events, got)
		return nil
	}); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(events) != 1 || events[0].ID() != event.ID() {
		t.Fatalf("round-trip events = %#v", events)
	}

	eventsPath := filepath.Join(root, string(manifest.ID), "events.ndjson.gz")
	file, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile() error = %v", err)
	}
	_, _ = file.Write([]byte("corruption"))
	_ = file.Close()
	if _, err := repository.Open(context.Background(), manifest.ID); !errors.Is(err, storage.ErrDatasetCorrupt) {
		t.Fatalf("Open() after corruption error = %v, want ErrDatasetCorrupt", err)
	}
}

func TestFileSourceRejectsInvalidFixture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.ndjson")
	if err := os.WriteFile(path, []byte("{\"Kind\":\"QUOTE\",\"unexpected\":true}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	err := (Source{Path: path}).Stream(context.Background(), marketdata.SourceQuery{},
		func(context.Context, marketdata.Observation) error { return nil })
	if err == nil {
		t.Fatal("Stream() error = nil, want invalid fixture error")
	}
}

func TestFileSourceReadsVersionedFixture(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "tests", "testdata", "marketdata", "observations.ndjson")
	var count int
	err := (Source{Path: path}).Stream(context.Background(), marketdata.SourceQuery{},
		func(_ context.Context, observation marketdata.Observation) error {
			count++
			if observation.Provider != "fixture" || observation.SourcePosition != int64(count) {
				t.Fatalf("observation %d = %#v", count, observation)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}
}

func TestPublicationGenerationsIgnoreIncompleteDirectories(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repository := Repository{Root: root}
	create := func(source string, parent storage.DatasetID, reason, requestID string) storage.DatasetManifest {
		writer, err := repository.Create(ctx, storage.DraftManifest{
			ParentID: parent, MasterVersion: "master-v1", CalendarVersion: "calendar-v1",
			Source: source, OrderingVersion: "ordering-v1", CreatedAt: time.Now().UTC(),
			CorrectionReason: reason, RequestID: requestID, Series: "series",
		})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if err := writer.Append(ctx, testQuote(t)); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
		manifest, err := writer.Commit(ctx)
		if err != nil {
			t.Fatalf("Commit() error = %v", err)
		}
		return manifest
	}
	parent := create("root", "", "", "")
	child := create("child", parent.ID, "correction", "correction-1")
	first, err := repository.Publish(ctx, storage.PublicationRequest{
		Series: "series", DatasetID: parent.ID, Action: storage.PublicationPublish,
		Reason: "initial", RequestID: "publication-1", PublishedAt: time.Unix(1, 0),
	})
	if err != nil {
		t.Fatalf("Publish(parent) error = %v", err)
	}
	incomplete := filepath.Join(root, "publications", "series", ".00000000000000000002.tmp")
	if err := os.MkdirAll(incomplete, 0o700); err != nil {
		t.Fatal(err)
	}
	second, err := repository.Publish(ctx, storage.PublicationRequest{
		Series: "series", DatasetID: child.ID, ExpectedCurrentID: parent.ID,
		Action: storage.PublicationPublish, Reason: "correction", RequestID: "publication-2",
		PublishedAt: time.Unix(2, 0),
	})
	if err != nil || second.Generation != first.Generation+1 {
		t.Fatalf("Publish(child) = %#v, %v", second, err)
	}
	current, err := repository.CurrentPublication(ctx, "series")
	if err != nil || current.DatasetID != child.ID {
		t.Fatalf("CurrentPublication() = %#v, %v", current, err)
	}
	reader, err := repository.Open(ctx, current.DatasetID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	count := 0
	engine := replay.NewEngine(replay.NewManualClock(time.Unix(0, 0)), nil)
	if err := engine.Replay(ctx, reader, replay.Request{Rate: replay.MaximumRate()},
		func(context.Context, model.Event) error {
			count++
			return nil
		}); err != nil || count != 1 {
		t.Fatalf("Replay() count = %d, error = %v", count, err)
	}
}

func testQuote(t *testing.T) model.QuoteEvent {
	t.Helper()
	id, _ := domain.InstrumentIDFromCanonicalKey("instrument")
	price, _ := domain.NewPrice(10000, "INR")
	at := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	event, err := model.NewQuoteEvent(model.QuoteSpec{
		InstrumentID: id, LastPrice: price, Volume: 1,
		ExchangeTime: at, IngestedAt: at.Add(time.Millisecond),
		Provenance: model.Provenance{Provider: "fixture", ProviderToken: "1", MasterVersion: "v1"},
	})
	if err != nil {
		t.Fatalf("NewQuoteEvent() error = %v", err)
	}
	return event
}

func BenchmarkRepositoryScan1000Events(b *testing.B) {
	repository := Repository{Root: b.TempDir()}
	base := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	writer, err := repository.Create(context.Background(), storage.DraftManifest{
		MasterVersion: "benchmark", CalendarVersion: "calendar-v1", Source: "benchmark",
		OrderingVersion: "v1", CreatedAt: base,
	})
	if err != nil {
		b.Fatal(err)
	}
	id, _ := domain.InstrumentIDFromCanonicalKey("benchmark-instrument")
	for index := 0; index < 1000; index++ {
		price, _ := domain.NewPrice(int64(10000+index), "INR")
		event, _ := model.NewQuoteEvent(model.QuoteSpec{
			InstrumentID: id, LastPrice: price,
			ExchangeTime: base.Add(time.Duration(index) * time.Millisecond),
			IngestedAt:   base.Add(time.Duration(index)*time.Millisecond + time.Microsecond),
			Provenance: model.Provenance{
				Provider: "benchmark", ProviderToken: "1", MasterVersion: "benchmark",
				SourceSequence: uint64(index), HasSequence: true,
			},
		})
		if err := writer.Append(context.Background(), event); err != nil {
			b.Fatal(err)
		}
	}
	manifest, err := writer.Commit(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	reader, err := repository.Open(context.Background(), manifest.ID)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		count := 0
		if err := reader.Scan(context.Background(), storage.EventQuery{},
			func(context.Context, model.Event) error {
				count++
				return nil
			}); err != nil {
			b.Fatal(err)
		}
		if count != 1000 {
			b.Fatalf("count = %d", count)
		}
	}
}
