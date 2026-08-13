package marketvalidation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const ZerodhaPreflightEvidenceSchemaVersion = "market-validation-zerodha-preflight/v1"

const TelegramEvidenceSchemaVersion = "market-validation-telegram-check/v1"

type ZerodhaPreflightEvidence struct {
	SchemaVersion           string    `json:"schema_version"`
	ApplicationCommit       string    `json:"application_commit"`
	TradingDate             string    `json:"trading_date"`
	Mode                    string    `json:"mode"`
	RuntimeBundleChecksum   string    `json:"runtime_bundle_checksum"`
	Timestamp               time.Time `json:"timestamp"`
	AuthenticationPass      bool      `json:"authentication_pass"`
	RESTAuthPass            bool      `json:"rest_auth_pass"`
	WebSocketAuthPass       bool      `json:"websocket_auth_pass"`
	ExpectedTokenCount      int       `json:"expected_token_count"`
	ExpectedTokensValid     bool      `json:"expected_tokens_valid"`
	ObservationsReceived    int       `json:"observations_received"`
	FreshObservations       uint64    `json:"fresh_observations"`
	ShutdownPass            bool      `json:"shutdown_pass"`
	TextMessagesReceived    uint64    `json:"text_messages_received"`
	BrokerMessagesReceived  uint64    `json:"broker_messages_received"`
	InstrumentsMetaReceived uint64    `json:"instruments_meta_received"`
	AppCodeReceived         uint64    `json:"app_code_received"`
	OrderUpdatesReceived    uint64    `json:"order_updates_received"`
	ProviderErrorsReceived  uint64    `json:"provider_errors_received"`
	BinaryFramesReceived    uint64    `json:"binary_frames_received"`
	HeartbeatsReceived      uint64    `json:"heartbeats_received"`
	PacketsReceived         uint64    `json:"packets_received"`
	IndexPacketsReceived    uint64    `json:"index_packets_received"`
	PacketsDecoded          uint64    `json:"packets_decoded"`
	PacketsRejected         uint64    `json:"packets_rejected"`
	TokenMatches            uint64    `json:"token_matches"`
	LastFailureStage        string    `json:"last_failure_stage"`
	AccessTokenExpiresAt    time.Time `json:"access_token_expires_at"`
}

func EncodeZerodhaPreflightEvidence(value ZerodhaPreflightEvidence) ([]byte, error) {
	if err := validateZerodhaPreflightEvidence(value); err != nil {
		return nil, err
	}
	return Marshal(value)
}

func PublishEvidenceCreateOnce(path string, raw []byte) (string, error) {
	clean := filepath.Clean(path)
	lower := strings.ToLower(clean)
	if clean == "." || len(raw) == 0 || strings.Contains(lower, "secret") || strings.Contains(lower, "credential") || strings.Contains(lower, "access_token") || strings.Contains(lower, "api_key") {
		return "", ErrInvalidRecord
	}
	if err := os.MkdirAll(filepath.Dir(clean), 0o750); err != nil {
		return "", err
	}
	if existing, err := os.ReadFile(clean); err == nil {
		if !bytes.Equal(existing, raw) {
			return "", ErrInvalidRecord
		}
		return digest(raw), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	temporary, err := os.CreateTemp(filepath.Dir(clean), ".tradeedge-evidence-*")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0o640); err == nil {
		_, err = temporary.Write(raw)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err = os.Link(temporaryPath, clean); err != nil {
		if existing, readErr := os.ReadFile(clean); readErr == nil && bytes.Equal(existing, raw) {
			return digest(raw), nil
		}
		return "", err
	}
	return digest(raw), nil
}

func decodeZerodhaPreflightEvidence(raw []byte) (ZerodhaPreflightEvidence, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value ZerodhaPreflightEvidence
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return ZerodhaPreflightEvidence{}, ErrInvalidRecord
	}
	if err := validateZerodhaPreflightEvidence(value); err != nil {
		return ZerodhaPreflightEvidence{}, err
	}
	return value, nil
}

func validateTelegramEvidenceRaw(raw []byte, date, mode string) error {
	var value struct {
		SchemaVersion string    `json:"schema_version"`
		TradingDate   string    `json:"trading_date"`
		Mode          string    `json:"mode"`
		Kind          string    `json:"kind"`
		Delivered     bool      `json:"delivered"`
		CheckedAt     time.Time `json:"checked_at"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF || value.SchemaVersion != TelegramEvidenceSchemaVersion || value.TradingDate != date || value.Mode != mode || value.Kind != "test" || !value.Delivered || value.CheckedAt.IsZero() {
		return ErrInvalidRecord
	}
	return nil
}

func validateZerodhaPreflightEvidence(value ZerodhaPreflightEvidence) error {
	location := time.FixedZone("IST", 5*60*60+30*60)
	expected := 2
	requiredIndexPackets := 2
	if value.Mode == "SHADOW" {
		expected = 14
	}
	if value.SchemaVersion != ZerodhaPreflightEvidenceSchemaVersion || !validCommit(value.ApplicationCommit) || (value.Mode != "PAPER" && value.Mode != "SHADOW") || !validDigest(strings.ToLower(value.RuntimeBundleChecksum)) || value.Timestamp.IsZero() ||
		!value.AuthenticationPass || !value.RESTAuthPass || !value.WebSocketAuthPass || value.ExpectedTokenCount != expected || !value.ExpectedTokensValid || value.ObservationsReceived != expected || value.FreshObservations != uint64(expected) || !value.ShutdownPass ||
		value.BinaryFramesReceived == 0 || value.PacketsReceived < uint64(expected) || value.IndexPacketsReceived < uint64(requiredIndexPackets) || value.PacketsDecoded < uint64(expected) || value.PacketsRejected != 0 || value.TokenMatches != uint64(expected) || value.LastFailureStage != "NONE" || !value.AccessTokenExpiresAt.After(value.Timestamp) || value.Timestamp.In(location).Format("2006-01-02") != value.TradingDate {
		return ErrInvalidRecord
	}
	if _, err := time.Parse("2006-01-02", value.TradingDate); err != nil {
		return ErrInvalidRecord
	}
	return nil
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
