package zerodha

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPersistAccessTokenPreservesOtherDotenvEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	original := "FIRST=one\n# keep comment\n" + accessTokenKey + "=old\nMIDDLE='two'\n" + accessExpiryKey + "=2026-08-12T00:30:00Z\nLAST=three\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	expiry := time.Date(2026, 8, 14, 0, 30, 0, 0, time.UTC)
	if err := persistAccessToken(path, "new-secret-token", expiry); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, preserved := range []string{"FIRST=one\n", "# keep comment\n", "MIDDLE='two'\n", "LAST=three\n"} {
		if !strings.Contains(text, preserved) {
			t.Fatalf("missing preserved entry %q", preserved)
		}
	}
	if strings.Count(text, accessTokenKey+"=") != 1 || strings.Count(text, accessExpiryKey+"=") != 1 || !strings.Contains(text, accessTokenKey+"=new-secret-token\n") || !strings.Contains(text, accessExpiryKey+"="+expiry.Format(time.RFC3339)+"\n") {
		t.Fatal("session fields were not replaced exactly once")
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(path)
		if statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode=%v err=%v", info.Mode().Perm(), statErr)
		}
	}
}

func TestPersistAccessTokenRejectsMalformedDotenvWithoutLeakingSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(accessTokenKey+"=one\n"+accessTokenKey+"=two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := "recognizable-new-secret"
	err := persistAccessToken(path, secret, time.Now().Add(time.Hour))
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("err=%v", err)
	}
}

func TestLookupWithPersistedSessionRequiresCompleteValidPair(t *testing.T) {
	base := func(key string) (string, bool) {
		values := map[string]string{"UNCHANGED": "base"}
		value, ok := values[key]
		return value, ok
	}
	for name, content := range map[string]string{
		"missing expiry": accessTokenKey + "=token\n",
		"bad expiry":     accessTokenKey + "=token\n" + accessExpiryKey + "=not-a-time\n",
		"duplicate":      accessTokenKey + "=one\n" + accessTokenKey + "=two\n" + accessExpiryKey + "=2026-08-14T00:30:00Z\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".env")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			lookup, err := LookupWithPersistedSession(base, path)
			if err == nil {
				_, loadErr := (EnvCredentialSource{Lookup: lookup}).Load(context.Background())
				if loadErr == nil {
					t.Fatal("malformed persisted credentials accepted")
				}
			}
		})
	}
	t.Run("empty placeholders use base", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".env")
		if err := os.WriteFile(path, []byte(accessTokenKey+"=\n"+accessExpiryKey+"=\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		lookup, err := LookupWithPersistedSession(base, path)
		if err != nil {
			t.Fatal(err)
		}
		if value, ok := lookup("UNCHANGED"); !ok || value != "base" {
			t.Fatalf("base lookup=%q,%t", value, ok)
		}
	})
}
