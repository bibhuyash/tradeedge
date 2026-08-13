package marketvalidation

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validPreflightEvidence() ZerodhaPreflightEvidence {
	now := time.Date(2026, 8, 11, 4, 30, 0, 0, time.UTC)
	return ZerodhaPreflightEvidence{
		SchemaVersion: ZerodhaPreflightEvidenceSchemaVersion, ApplicationCommit: strings.Repeat("b", 40), TradingDate: "2026-08-11", Mode: "PAPER",
		RuntimeBundleChecksum: strings.Repeat("a", 64), Timestamp: now, AuthenticationPass: true, RESTAuthPass: true, WebSocketAuthPass: true,
		ExpectedTokenCount: 2, ExpectedTokensValid: true, ObservationsReceived: 2, FreshObservations: 2, ShutdownPass: true,
		TextMessagesReceived: 2, InstrumentsMetaReceived: 1, AppCodeReceived: 1, BinaryFramesReceived: 1, PacketsReceived: 2,
		IndexPacketsReceived: 2, PacketsDecoded: 2, TokenMatches: 2, LastFailureStage: "NONE", AccessTokenExpiresAt: now.Add(12 * time.Hour),
	}
}

func TestZerodhaPreflightEvidenceRejectsNonPassingEvidence(t *testing.T) {
	value := validPreflightEvidence()
	if raw, err := EncodeZerodhaPreflightEvidence(value); err != nil || len(raw) == 0 {
		t.Fatalf("valid evidence: %v", err)
	}
	value.AuthenticationPass = false
	if _, err := EncodeZerodhaPreflightEvidence(value); err == nil {
		t.Fatal("failed authentication was accepted")
	}
	value = validPreflightEvidence()
	value.PacketsRejected = 1
	if _, err := EncodeZerodhaPreflightEvidence(value); err == nil {
		t.Fatal("rejected packet was accepted")
	}
}

func TestZerodhaPreflightEvidenceAcceptsBoundedShadowUniverse(t *testing.T) {
	value := validPreflightEvidence()
	value.Mode = "SHADOW"
	value.ExpectedTokenCount = 14
	value.ObservationsReceived = 14
	value.FreshObservations = 14
	value.BinaryFramesReceived = 14
	value.PacketsReceived = 14
	value.IndexPacketsReceived = 2
	value.PacketsDecoded = 14
	value.TokenMatches = 14
	raw, err := EncodeZerodhaPreflightEvidence(value)
	if err != nil || len(raw) == 0 {
		t.Fatalf("EncodeZerodhaPreflightEvidence() bytes=%d err=%v", len(raw), err)
	}
}

func TestPublishEvidenceCreateOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preflight.json")
	raw := []byte("safe-evidence")
	want := sha256.Sum256(raw)
	wantChecksum := hex.EncodeToString(want[:])
	checksum, err := PublishEvidenceCreateOnce(path, raw)
	if err != nil || checksum != wantChecksum {
		t.Fatalf("publish: checksum=%q err=%v", checksum, err)
	}
	if second, err := PublishEvidenceCreateOnce(path, raw); err != nil || second != checksum {
		t.Fatalf("idempotent publish: checksum=%q err=%v", second, err)
	}
	if _, err := PublishEvidenceCreateOnce(path, []byte("different")); err == nil {
		t.Fatal("create-once artifact was overwritten")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("read artifact: %q err=%v", got, err)
	}
}

func TestAuthorizationExternalEvidenceBindsCommitBundleDateAndChecksums(t *testing.T) {
	directory := t.TempDir()
	telegramRaw := []byte(`{"schema_version":"market-validation-telegram-check/v1","trading_date":"2026-08-11","mode":"PAPER","kind":"test","delivered":true,"checked_at":"2026-08-11T04:00:00Z"}`)
	preflightRaw, err := EncodeZerodhaPreflightEvidence(validPreflightEvidence())
	if err != nil {
		t.Fatal(err)
	}
	telegramPath, preflightPath := filepath.Join(directory, "telegram.json"), filepath.Join(directory, "preflight.json")
	if err := os.WriteFile(telegramPath, telegramRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preflightPath, preflightRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	artifact := func(name string, raw []byte) AuthorizedArtifact {
		sum := sha256.Sum256(raw)
		checksum := hex.EncodeToString(sum[:])
		return AuthorizedArtifact{Path: name, SHA256: checksum, Identity: checksum}
	}
	value := authorizationShape(ScopeOperationsOnly)
	value.TradingDate = "2026-08-11"
	value.ApplicationCommit = strings.Repeat("b", 40)
	value.Artifacts.RuntimeBundle.SHA256 = strings.Repeat("a", 64)
	value.Artifacts.TelegramEvidence = artifact("telegram.json", telegramRaw)
	value.Artifacts.ZerodhaPreflight = artifact("preflight.json", preflightRaw)
	if err := validateAuthorizationExternalEvidence(directory, value); err != nil {
		t.Fatalf("valid external evidence: %v", err)
	}
	value.ApplicationCommit = strings.Repeat("c", 40)
	if err := validateAuthorizationExternalEvidence(directory, value); err == nil {
		t.Fatal("commit mismatch accepted")
	}
	value.ApplicationCommit = strings.Repeat("b", 40)
	value.Artifacts.RuntimeBundle.SHA256 = strings.Repeat("d", 64)
	if err := validateAuthorizationExternalEvidence(directory, value); err == nil {
		t.Fatal("runtime bundle mismatch accepted")
	}
	value.Artifacts.RuntimeBundle.SHA256 = strings.Repeat("a", 64)
	value.Artifacts.TelegramEvidence.SHA256 = strings.Repeat("e", 64)
	value.Artifacts.TelegramEvidence.Identity = strings.Repeat("e", 64)
	if err := validateAuthorizationExternalEvidence(directory, value); err == nil {
		t.Fatal("Telegram checksum mismatch accepted")
	}
}
