package sessionfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/session"
)

func TestStoreRoundTripReplaceDeleteAndCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "zerodha.json")
	store, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(context.Background()); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("missing load error = %v", err)
	}
	now := time.Date(2026, 9, 18, 4, 0, 0, 0, time.UTC)
	first := session.Record{Provider: session.ProviderZerodha, AccessToken: "first", AuthenticatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err = store.Save(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.AccessToken = "second"
	if err = store.Save(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil || loaded != second {
		t.Fatalf("Load() = %#v, %v", loaded, err)
	}
	if err = store.Delete(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(context.Background()); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("deleted load error = %v", err)
	}
	if err = os.WriteFile(path, []byte(`{"schema_version":"other"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(context.Background()); !errors.Is(err, session.ErrCorrupt) {
		t.Fatalf("corrupt load error = %v", err)
	}
}
