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

	"github.com/bibhuyash/tradeedge/internal/adapters/marketdata/calendarfile"
	"github.com/bibhuyash/tradeedge/internal/config"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	portfolioconfig "github.com/bibhuyash/tradeedge/internal/portfolio/config"
	riskconfig "github.com/bibhuyash/tradeedge/internal/risk/config"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	"github.com/bibhuyash/tradeedge/internal/risk/rules"
)

const (
	AuthorizationSchemaVersion       = "market-validation-authorization/v2"
	ShadowAuthorizationSchemaVersion = "market-validation-shadow-authorization/v3"
)

var ErrStrategyBlocked = errors.New("STRATEGY_BLOCKED")

type AuthorizedArtifact struct {
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Identity string `json:"identity"`
}

type AuthorizationArtifacts struct {
	RuntimeBundle          AuthorizedArtifact  `json:"runtime_bundle"`
	Calendar               AuthorizedArtifact  `json:"calendar"`
	CalendarApproval       AuthorizedArtifact  `json:"calendar_approval"`
	InstrumentMaster       AuthorizedArtifact  `json:"instrument_master"`
	Watchlist              AuthorizedArtifact  `json:"watchlist"`
	Strategies             AuthorizedArtifact  `json:"strategies"`
	Portfolio              AuthorizedArtifact  `json:"portfolio"`
	Risk                   AuthorizedArtifact  `json:"risk"`
	TelegramEvidence       AuthorizedArtifact  `json:"telegram_evidence"`
	ZerodhaPreflight       AuthorizedArtifact  `json:"zerodha_preflight"`
	PrerequisiteDay0Gate   *AuthorizedArtifact `json:"prerequisite_day0_gate,omitempty"`
	QualificationNIFTY     *AuthorizedArtifact `json:"qualification_nifty,omitempty"`
	QualificationBANKNIFTY *AuthorizedArtifact `json:"qualification_banknifty,omitempty"`
}

type AuthorizedStrategy struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	Classification    string `json:"classification"`
	ConfigurationHash string `json:"configuration_hash"`
	CASPolicy         string `json:"cas_policy"`
	Enabled           bool   `json:"enabled"`
}

type AuthorizationManifest struct {
	SchemaVersion                string                 `json:"schema_version"`
	Checksum                     string                 `json:"checksum"`
	ApplicationCommit            string                 `json:"application_commit"`
	Mode                         string                 `json:"mode"`
	Scope                        Scope                  `json:"scope"`
	TradingDate                  string                 `json:"trading_date"`
	AuthorizedAt                 time.Time              `json:"authorized_at"`
	ExpiresAt                    time.Time              `json:"expires_at"`
	ApprovedBy                   string                 `json:"approved_by"`
	EvidenceRoot                 string                 `json:"evidence_root"`
	PaperCapitalMinor            int64                  `json:"paper_capital_minor"`
	Currency                     string                 `json:"currency"`
	PortfolioID                  string                 `json:"portfolio_id"`
	Strategy                     AuthorizedStrategy     `json:"strategy"`
	Artifacts                    AuthorizationArtifacts `json:"artifacts"`
	LiveTradingAuthorized        bool                   `json:"live_trading_authorized"`
	RealBrokerMutationProhibited bool                   `json:"real_broker_mutation_prohibited"`
	PaperExecutionProhibited     bool                   `json:"paper_execution_prohibited"`
	QualificationEnabled         bool                   `json:"qualification_enabled"`
	ApprovedUnderlyings          []string               `json:"approved_underlyings,omitempty"`
}

func DecodeAuthorization(raw []byte) (AuthorizationManifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value AuthorizationManifest
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return AuthorizationManifest{}, ErrInvalidRecord
	}
	return value, nil
}

func FinalizeAuthorization(path string, input AuthorizationManifest) (AuthorizationManifest, error) {
	input.SchemaVersion, input.Checksum, input.LiveTradingAuthorized = AuthorizationSchemaVersion, "", false
	if input.Mode == "SHADOW" {
		input.SchemaVersion, input.RealBrokerMutationProhibited, input.PaperExecutionProhibited, input.QualificationEnabled = ShadowAuthorizationSchemaVersion, true, true, true
	}
	if err := validateAuthorizationShape(input); err != nil {
		return AuthorizationManifest{}, err
	}
	if err := validateAuthorizationFiles(path, input); err != nil {
		return AuthorizationManifest{}, err
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return AuthorizationManifest{}, err
	}
	sum := sha256.Sum256(raw)
	input.Checksum = hex.EncodeToString(sum[:])
	return input, nil
}

func VerifyAuthorization(path string, value AuthorizationManifest) error {
	want := strings.ToLower(value.Checksum)
	if !validDigest(want) || value.LiveTradingAuthorized {
		return ErrInvalidRecord
	}
	value.Checksum = ""
	rebuilt, err := FinalizeAuthorization(path, value)
	if err != nil || rebuilt.Checksum != want {
		return ErrInvalidRecord
	}
	return nil
}

func LoadAuthorization(path string) (AuthorizationManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return AuthorizationManifest{}, err
	}
	value, err := DecodeAuthorization(raw)
	if err != nil {
		return AuthorizationManifest{}, err
	}
	if err := VerifyAuthorization(path, value); err != nil {
		return AuthorizationManifest{}, err
	}
	return value, nil
}

func validateAuthorizationShape(value AuthorizationManifest) error {
	validSchema := value.Mode == "PAPER" && value.SchemaVersion == AuthorizationSchemaVersion || value.Mode == "SHADOW" && value.SchemaVersion == ShadowAuthorizationSchemaVersion
	if !validSchema || !validCommit(value.ApplicationCommit) || (value.Mode != "PAPER" && value.Mode != "SHADOW") ||
		(value.Scope != ScopeOperationsOnly && value.Scope != ScopeFullPipeline && value.Scope != ScopeQualificationOnly) || value.LiveTradingAuthorized || (value.Mode == "SHADOW" && !value.RealBrokerMutationProhibited) ||
		value.AuthorizedAt.IsZero() || !value.ExpiresAt.After(value.AuthorizedAt) || value.ExpiresAt.Sub(value.AuthorizedAt) > 24*time.Hour ||
		strings.TrimSpace(value.ApprovedBy) == "" || strings.TrimSpace(value.EvidenceRoot) == "" || unsafeIdentity(value.EvidenceRoot) ||
		(value.Mode == "PAPER" && value.PaperCapitalMinor != 100000000) || (value.Mode == "SHADOW" && value.PaperCapitalMinor != 0) || value.Currency != "INR" || value.PortfolioID == "" {
		return ErrInvalidRecord
	}
	date, err := time.Parse("2006-01-02", value.TradingDate)
	if err != nil {
		return ErrInvalidRecord
	}
	location := time.FixedZone("IST", 5*60*60+30*60)
	authorizedDate, expiryDate := value.AuthorizedAt.In(location).Format("2006-01-02"), value.ExpiresAt.In(location).Format("2006-01-02")
	if authorizedDate != date.Format("2006-01-02") || expiryDate != value.TradingDate {
		return ErrInvalidRecord
	}
	for _, artifact := range authorizationArtifacts(value) {
		if strings.TrimSpace(artifact.Path) == "" || !validDigest(strings.ToLower(artifact.SHA256)) || strings.TrimSpace(artifact.Identity) == "" || unsafeIdentity(artifact.Path) {
			return ErrInvalidRecord
		}
	}
	strategy := value.Strategy
	if value.Mode == "SHADOW" {
		if value.Scope != ScopeQualificationOnly || !value.PaperExecutionProhibited || !value.QualificationEnabled || value.Artifacts.PrerequisiteDay0Gate != nil ||
			value.Artifacts.QualificationNIFTY == nil || value.Artifacts.QualificationBANKNIFTY == nil || strategy.Name != "EMA_REFERENCE_V1" || strategy.Version != "1" ||
			strategy.Classification != "REFERENCE_CANDIDATE" || !strategy.Enabled || strategy.CASPolicy != "CAS_RESTRICTED" || !validDigest(strategy.ConfigurationHash) ||
			len(value.ApprovedUnderlyings) != 2 || value.ApprovedUnderlyings[0] != "BANKNIFTY" || value.ApprovedUnderlyings[1] != "NIFTY" {
			return ErrStrategyBlocked
		}
	} else if value.PaperExecutionProhibited || value.QualificationEnabled || len(value.ApprovedUnderlyings) != 0 || value.Artifacts.QualificationNIFTY != nil || value.Artifacts.QualificationBANKNIFTY != nil {
		return ErrInvalidRecord
	} else if value.Scope == ScopeOperationsOnly {
		if value.Artifacts.PrerequisiteDay0Gate != nil || strategy.Name != "NONE" || strategy.Enabled || strategy.Classification != "NONE" || strategy.Version != "strategies-disabled/v1" || strategy.CASPolicy != "CAS_DISABLED" || !validDigest(strategy.ConfigurationHash) {
			return ErrInvalidRecord
		}
	} else if !strategy.Enabled || strategy.Name == "" || strategy.Version == "" || strategy.Classification != "PRODUCTION_CANDIDATE" ||
		(strategy.CASPolicy != "CAS_SAFE" && strategy.CASPolicy != "CAS_RESTRICTED") || !validDigest(strategy.ConfigurationHash) || value.Artifacts.PrerequisiteDay0Gate == nil {
		return ErrStrategyBlocked
	}
	return nil
}

func validateAuthorizationFiles(manifestPath string, value AuthorizationManifest) error {
	base := filepath.Dir(manifestPath)
	for _, artifact := range authorizationArtifacts(value) {
		if err := verifyArtifact(base, artifact); err != nil {
			return err
		}
	}
	bundlePath := resolveAuthorizationPath(base, value.Artifacts.RuntimeBundle.Path)
	bundle, err := config.LoadRuntimeBundle(bundlePath)
	if err != nil {
		return err
	}
	if bundle.Manifest.Mode != value.Mode || bundle.Checksum != value.Artifacts.RuntimeBundle.Identity || bundle.Watchlist.Version != value.Artifacts.Watchlist.Identity ||
		value.Strategy.ConfigurationHash != strings.ToLower(value.Artifacts.Strategies.SHA256) || value.Strategy.Version != value.Artifacts.Strategies.Identity {
		return ErrInvalidRecord
	}
	bundleBase := filepath.Dir(bundlePath)
	checks := []struct {
		name string
		ref  config.FileReference
		art  AuthorizedArtifact
	}{
		{"calendar", bundle.Manifest.Calendar, value.Artifacts.Calendar},
		{"instrument_master", bundle.Manifest.InstrumentMaster, value.Artifacts.InstrumentMaster},
		{"watchlist", bundle.Manifest.Watchlist, value.Artifacts.Watchlist},
		{"strategies", bundle.Manifest.Strategies, value.Artifacts.Strategies},
		{"portfolio", bundle.Manifest.Portfolio, value.Artifacts.Portfolio},
		{"risk", bundle.Manifest.Risk, value.Artifacts.Risk},
	}
	if value.Mode == "SHADOW" {
		checks = append(checks,
			struct {
				name string
				ref  config.FileReference
				art  AuthorizedArtifact
			}{"qualification_nifty", *bundle.Manifest.QualificationNIFTY, *value.Artifacts.QualificationNIFTY},
			struct {
				name string
				ref  config.FileReference
				art  AuthorizedArtifact
			}{"qualification_banknifty", *bundle.Manifest.QualificationBANKNIFTY, *value.Artifacts.QualificationBANKNIFTY},
		)
	}
	for _, check := range checks {
		left, _ := filepath.Abs(resolveAuthorizationPath(bundleBase, check.ref.Path))
		right, _ := filepath.Abs(resolveAuthorizationPath(base, check.art.Path))
		if !strings.EqualFold(filepath.Clean(left), filepath.Clean(right)) || !strings.EqualFold(check.ref.SHA256, check.art.SHA256) {
			return ErrInvalidRecord
		}
	}
	schedule, err := calendarfile.Load(resolveAuthorizationPath(base, value.Artifacts.Calendar.Path))
	if err != nil || string(schedule.Version()) != value.Artifacts.Calendar.Identity {
		return ErrInvalidRecord
	}
	var calendarApproval CalendarApproval
	if err := decodeStrictPath(resolveAuthorizationPath(base, value.Artifacts.CalendarApproval.Path), &calendarApproval); err != nil ||
		!calendarApproval.Approved || calendarApproval.LiveTradingAuthorized || calendarApproval.CalendarVersion != value.Artifacts.Calendar.Identity ||
		calendarApproval.CalendarSHA256 != strings.ToLower(value.Artifacts.Calendar.SHA256) || calendarApproval.CalendarSHA256 != value.Artifacts.CalendarApproval.Identity {
		return ErrInvalidRecord
	}
	master, _, err := instrumentmaster.LoadFile(resolveAuthorizationPath(base, value.Artifacts.InstrumentMaster.Path))
	if err != nil || string(master.Version()) != value.Artifacts.InstrumentMaster.Identity {
		return ErrInvalidRecord
	}
	portfolioRaw, err := os.ReadFile(resolveAuthorizationPath(base, value.Artifacts.Portfolio.Path))
	if err != nil {
		return err
	}
	portfolio, err := portfolioconfig.Decode(portfolioRaw)
	capitalMatches := value.Mode == "SHADOW" || portfolio.AllocationPolicy().Limits.TotalCapital.MinorUnits() == value.PaperCapitalMinor
	if err != nil || portfolio.ID().String() != value.PortfolioID || portfolio.Hash().String() != value.Artifacts.Portfolio.Identity || !capitalMatches {
		return ErrInvalidRecord
	}
	riskRaw, err := os.ReadFile(resolveAuthorizationPath(base, value.Artifacts.Risk.Path))
	if err != nil {
		return err
	}
	descriptors := map[riskmodel.RiskRuleID]riskmodel.RiskRuleDescriptor{}
	for _, rule := range rules.ProductionCatalog() {
		descriptors[rule.Descriptor().ID] = rule.Descriptor()
	}
	risk, err := riskconfig.Decode(riskRaw, descriptors, portfolio.AllocationPolicy().Limits.ExposureGroups)
	if err != nil || rules.ValidateProductionPolicy(risk.Policy()) != nil || risk.Hash().String() != value.Artifacts.Risk.Identity {
		return ErrInvalidRecord
	}
	if err := validateAuthorizationExternalEvidence(base, value); err != nil {
		return err
	}
	if value.Artifacts.PrerequisiteDay0Gate != nil {
		var day0 GateReport
		if err := decodeStrictPath(resolveAuthorizationPath(base, value.Artifacts.PrerequisiteDay0Gate.Path), &day0); err != nil ||
			day0.SchemaVersion != Day0GateSchemaVersion || !day0.Passed || day0.LiveTradingAuthorized || day0.EvidenceSHA256 != value.Artifacts.PrerequisiteDay0Gate.Identity {
			return ErrInvalidRecord
		}
	}
	return nil
}

func validateAuthorizationExternalEvidence(base string, value AuthorizationManifest) error {
	for _, artifact := range []AuthorizedArtifact{value.Artifacts.TelegramEvidence, value.Artifacts.ZerodhaPreflight} {
		if err := verifyArtifact(base, artifact); err != nil {
			return ErrInvalidRecord
		}
	}
	telegramRaw, err := os.ReadFile(resolveAuthorizationPath(base, value.Artifacts.TelegramEvidence.Path))
	if err != nil || value.Artifacts.TelegramEvidence.Identity != strings.ToLower(value.Artifacts.TelegramEvidence.SHA256) || validateTelegramEvidenceRaw(telegramRaw, value.TradingDate, value.Mode) != nil {
		return ErrInvalidRecord
	}
	preflightRaw, err := os.ReadFile(resolveAuthorizationPath(base, value.Artifacts.ZerodhaPreflight.Path))
	preflight, preflightErr := decodeZerodhaPreflightEvidence(preflightRaw)
	if err != nil || preflightErr != nil || value.Artifacts.ZerodhaPreflight.Identity != strings.ToLower(value.Artifacts.ZerodhaPreflight.SHA256) || preflight.ApplicationCommit != value.ApplicationCommit || preflight.TradingDate != value.TradingDate || preflight.Mode != value.Mode || preflight.RuntimeBundleChecksum != strings.ToLower(value.Artifacts.RuntimeBundle.SHA256) {
		return ErrInvalidRecord
	}
	return nil
}

func authorizationArtifacts(value AuthorizationManifest) []AuthorizedArtifact {
	result := []AuthorizedArtifact{value.Artifacts.RuntimeBundle, value.Artifacts.Calendar, value.Artifacts.CalendarApproval, value.Artifacts.InstrumentMaster, value.Artifacts.Watchlist, value.Artifacts.Strategies, value.Artifacts.Portfolio, value.Artifacts.Risk, value.Artifacts.TelegramEvidence, value.Artifacts.ZerodhaPreflight}
	if value.Artifacts.PrerequisiteDay0Gate != nil {
		result = append(result, *value.Artifacts.PrerequisiteDay0Gate)
	}
	if value.Artifacts.QualificationNIFTY != nil {
		result = append(result, *value.Artifacts.QualificationNIFTY)
	}
	if value.Artifacts.QualificationBANKNIFTY != nil {
		result = append(result, *value.Artifacts.QualificationBANKNIFTY)
	}
	return result
}

func verifyArtifact(base string, artifact AuthorizedArtifact) error {
	raw, err := os.ReadFile(resolveAuthorizationPath(base, artifact.Path))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != strings.ToLower(artifact.SHA256) {
		return ErrInvalidRecord
	}
	return nil
}

func resolveAuthorizationPath(base, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(base, filepath.Clean(path))
}

func unsafeIdentity(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "secret") || strings.Contains(lower, "credential") || strings.Contains(lower, "access_token") || strings.Contains(lower, "api_key") || strings.Contains(lower, "bot_token") || strings.Contains(lower, "chat_id")
}
