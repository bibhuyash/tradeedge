package checkpointfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	platform "github.com/bibhuyash/tradeedge/internal/platform/checkpointfile"
	"github.com/bibhuyash/tradeedge/internal/shadowruntime"
)

func TestCleanShutdownCheckpointRestartsWithEquivalentState(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := checkpointSnapshot()
	published, err := store.Publish(context.Background(), snapshot, 0, "calendar/v1", configurationChecksum(), time.Now(), true)
	if err != nil {
		t.Fatal(err)
	}
	restored, generation, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if restored.Checksum != snapshot.Checksum || restored.Revision != snapshot.Revision {
		t.Fatalf("restored snapshot = %#v, want %#v", restored, snapshot)
	}
	if !generation.CleanShutdown || generation.Checksum != published.Checksum || generation.Sequence != 1 {
		t.Fatalf("restored generation = %#v", generation)
	}
}

func TestCleanShutdownCheckpointGenuineConflictFailsClosed(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := checkpointSnapshot()
	if _, err = store.Publish(context.Background(), snapshot, 0, "calendar/v1", configurationChecksum(), time.Now(), true); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Publish(context.Background(), snapshot, 0, "calendar/v1", configurationChecksum(), time.Now(), true); !errors.Is(err, platform.ErrConflict) {
		t.Fatalf("second publication error = %v, want conflict", err)
	}
}

func TestCleanShutdownCheckpointPersistenceFailureFailsClosed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "checkpoint-root")
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Publish(context.Background(), checkpointSnapshot(), 0, "calendar/v1", configurationChecksum(), time.Now(), true)
	if err == nil || errors.Is(err, platform.ErrConflict) {
		t.Fatalf("persistence error = %v, want non-conflict failure", err)
	}
}

func checkpointSnapshot() shadowruntime.Snapshot {
	return shadowruntime.Snapshot{
		SchemaVersion: shadowruntime.SchemaVersion,
		Revision:      42,
		TradingDate:   "2026-08-13",
		Checksum:      "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
}

func configurationChecksum() string {
	return "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
}
