package checkpointfile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorePublishesAndRestoresVerifiedGeneration(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	value := generation(1, false)
	published, err := store.Publish(context.Background(), value, 0)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := store.Load(context.Background())
	if err != nil || restored.Checksum != published.Checksum || restored.CleanShutdown {
		t.Fatalf("restored=%#v err=%v", restored, err)
	}
	clean := generation(2, true)
	if _, err := store.Publish(context.Background(), clean, 1); err != nil {
		t.Fatal(err)
	}
	restored, _ = store.Load(context.Background())
	if !restored.CleanShutdown || restored.Sequence != 2 {
		t.Fatalf("restored=%#v", restored)
	}
}

func TestStoreRejectsCorruptionConflictAndSecrets(t *testing.T) {
	root := t.TempDir()
	store, _ := New(root)
	published, err := store.Publish(context.Background(), generation(1, false), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Publish(context.Background(), generation(2, false), 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict=%v", err)
	}
	current, _ := os.ReadFile(filepath.Join(root, "CURRENT"))
	manifest := filepath.Join(root, "generations", strings.TrimSpace(string(current)), "manifest.json")
	raw, _ := os.ReadFile(manifest)
	if err := os.WriteFile(manifest, append(raw, 'x'), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corruption=%v, checksum=%s", err, published.Checksum)
	}
	bad := generation(1, false)
	bad.Components[0].Data = json.RawMessage(`{"access_token":"secret"}`)
	if _, err := NewGeneration(bad); !errors.Is(err, ErrSensitive) {
		t.Fatalf("secret=%v", err)
	}
}

func generation(sequence uint64, clean bool) Generation {
	sum := sha256.Sum256([]byte("configuration"))
	return Generation{SchemaVersion: SchemaVersion, Sequence: sequence, Mode: "PAPER", CalendarVersion: "calendar/v1", ConfigurationChecksum: hex.EncodeToString(sum[:]), CreatedAt: time.Date(2026, 8, 10, 4, int(sequence), 0, 0, time.UTC), CleanShutdown: clean, Components: []Component{{Name: "runtime", Revision: string(rune('0' + sequence)), Data: json.RawMessage(`{"state":"READY"}`)}}}
}
