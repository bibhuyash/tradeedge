package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	brokerzerodha "github.com/bibhuyash/tradeedge/internal/adapters/broker/zerodha"
	"github.com/bibhuyash/tradeedge/internal/adapters/sessionfile"
	"github.com/bibhuyash/tradeedge/internal/session"
)

type shadowSessionClock struct{ now time.Time }

func (c shadowSessionClock) Now() time.Time { return c.now }

func TestShadowSessionV2LoadsValidSessionAndFailsClosed(t *testing.T) {
	now := time.Date(2026, 9, 18, 4, 0, 0, 0, time.UTC)
	clock := shadowSessionClock{now: now}
	t.Run("valid", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "zerodha.json")
		store, err := sessionfile.New(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Save(context.Background(), session.Record{
			Provider: session.ProviderZerodha, AccessToken: "stored-access", AuthenticatedAt: now, ExpiresAt: now.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		manager, err := loadShadowSession(context.Background(), path, "public-key", clock)
		if err != nil {
			t.Fatal(err)
		}
		defer manager.Shutdown()
		authorization, err := manager.Authorization()
		if err != nil || authorization != "token public-key:stored-access" {
			t.Fatalf("Authorization() = %q, %v", authorization, err)
		}
	})

	tests := map[string]func(string) error{
		"missing": func(string) error { return nil },
		"corrupt": func(path string) error { return os.WriteFile(path, []byte("{"), 0o600) },
		"expired": func(path string) error {
			store, err := sessionfile.New(path)
			if err != nil {
				return err
			}
			return store.Save(context.Background(), session.Record{
				Provider: session.ProviderZerodha, AccessToken: "expired", AuthenticatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
			})
		},
	}
	for name, prepare := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "zerodha.json")
			if err := prepare(path); err != nil {
				t.Fatal(err)
			}
			_, err := loadShadowSession(context.Background(), path, "public-key", clock)
			if !errors.Is(err, brokerzerodha.ErrAuthentication) {
				t.Fatalf("loadShadowSession() error = %v", err)
			}
		})
	}
}
