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
	return AuthorizationManifest{SchemaVersion: AuthorizationSchemaVersion, ApplicationCommit: strings.Repeat("b", 40), Mode: "PAPER", Scope: scope, TradingDate: "2026-08-10", AuthorizedAt: time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC), ApprovedBy: "CEO", EvidenceRoot: ".cache/market-validation/2026-08-10", PaperCapitalMinor: 100000000, Currency: "INR", PortfolioID: "portfolio", Strategy: AuthorizedStrategy{Name: "NONE", Version: "strategies-disabled/v1", Classification: "NONE", ConfigurationHash: digest, CASPolicy: "CAS_DISABLED"}, Artifacts: AuthorizationArtifacts{RuntimeBundle: artifact, Calendar: artifact, CalendarApproval: artifact, InstrumentMaster: artifact, Watchlist: artifact, Strategies: artifact, Portfolio: artifact, Risk: artifact, TelegramEvidence: artifact, ZerodhaPreflight: artifact}}
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

func TestShadowAuthorizationShapeIsQualificationOnlyAndCannotAuthorizePaper(t *testing.T) {
	value := authorizationShape(ScopeQualificationOnly)
	digest := strings.Repeat("a", 64)
	artifact := AuthorizedArtifact{Path: "qualification.json", SHA256: digest, Identity: digest}
	value.SchemaVersion = ShadowAuthorizationSchemaVersion
	value.Mode = "SHADOW"
	value.PaperCapitalMinor = 0
	value.RealBrokerMutationProhibited = true
	value.PaperExecutionProhibited = true
	value.QualificationEnabled = true
	value.ApprovedUnderlyings = []string{"BANKNIFTY", "NIFTY"}
	value.Artifacts.QualificationNIFTY = &artifact
	value.Artifacts.QualificationBANKNIFTY = &artifact
	value.Strategy = AuthorizedStrategy{Name: "EMA_REFERENCE_V1", Version: "1", Classification: "REFERENCE_CANDIDATE", ConfigurationHash: digest, CASPolicy: "CAS_RESTRICTED", Enabled: true}
	if err := validateAuthorizationShape(value); err != nil {
		t.Fatalf("valid SHADOW shape rejected: %v", err)
	}
	value.Scope = ScopeOperationsOnly
	if err := validateAuthorizationShape(value); !errors.Is(err, ErrStrategyBlocked) {
		t.Fatalf("SHADOW PAPER scope = %v", err)
	}
	value.Scope = ScopeQualificationOnly
	value.Mode = "PAPER"
	if err := validateAuthorizationShape(value); err == nil {
		t.Fatal("SHADOW authorization accepted as PAPER")
	}
}
