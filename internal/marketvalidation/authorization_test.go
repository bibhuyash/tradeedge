package marketvalidation

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func authorizationShape(scope Scope) AuthorizationManifest {
	digest := strings.Repeat("a", 64)
	artifact := AuthorizedArtifact{Path: "artifact.json", SHA256: digest, Identity: "identity"}
	return AuthorizationManifest{SchemaVersion: AuthorizationSchemaVersion, ApplicationCommit: strings.Repeat("b", 40), Mode: "PAPER", Scope: scope, TradingDate: "2026-08-10", AuthorizedAt: time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC), ApprovedBy: "CEO", EvidenceRoot: ".cache/market-validation/2026-08-10", TelegramConfigurationIdentity: "telegram-outbound/v1", PaperCapitalMinor: 100000000, Currency: "INR", PortfolioID: "portfolio", Strategy: AuthorizedStrategy{Name: "NONE", Version: "strategies-disabled/v1", Classification: "NONE", ConfigurationHash: digest, CASPolicy: "CAS_DISABLED"}, Artifacts: AuthorizationArtifacts{RuntimeBundle: artifact, Calendar: artifact, CalendarApproval: artifact, InstrumentMaster: artifact, Watchlist: artifact, Strategies: artifact, Portfolio: artifact, Risk: artifact}}
}

func TestAuthorizationRejectsChecksumMismatchAndLiveFlag(t *testing.T) {
	value := authorizationShape(ScopeOperationsOnly)
	value.Checksum = strings.Repeat("c", 64)
	if err := VerifyAuthorization("manifest.json", value); err == nil {
		t.Fatal("manifest checksum mismatch accepted")
	}
	value.LiveTradingAuthorized = true
	if err := VerifyAuthorization("manifest.json", value); err == nil {
		t.Fatal("live authorization accepted")
	}
}

func TestFullPipelineAuthorizationWithNoneIsStrategyBlocked(t *testing.T) {
	value := authorizationShape(ScopeFullPipeline)
	if _, err := FinalizeAuthorization("manifest.json", value); !errors.Is(err, ErrStrategyBlocked) {
		t.Fatalf("got %v", err)
	}
}
