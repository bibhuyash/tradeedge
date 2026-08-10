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

const AuthorizationSchemaVersion = "market-validation-authorization/v1"

var ErrStrategyBlocked = errors.New("STRATEGY_BLOCKED")

type AuthorizedArtifact struct {
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Identity string `json:"identity"`
}

type AuthorizationArtifacts struct {
	RuntimeBundle        AuthorizedArtifact  `json:"runtime_bundle"`
	Calendar             AuthorizedArtifact  `json:"calendar"`
	CalendarApproval     AuthorizedArtifact  `json:"calendar_approval"`
	InstrumentMaster     AuthorizedArtifact  `json:"instrument_master"`
	Watchlist            AuthorizedArtifact  `json:"watchlist"`
	Strategies           AuthorizedArtifact  `json:"strategies"`
	Portfolio            AuthorizedArtifact  `json:"portfolio"`
	Risk                 AuthorizedArtifact  `json:"risk"`
	PrerequisiteDay0Gate *AuthorizedArtifact `json:"prerequisite_day0_gate,omitempty"`
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
	SchemaVersion                 string                 `json:"schema_version"`
	Checksum                      string                 `json:"checksum"`
	ApplicationCommit             string                 `json:"application_commit"`
	Mode                          string                 `json:"mode"`
	Scope                         Scope                  `json:"scope"`
	TradingDate                   string                 `json:"trading_date"`
	AuthorizedAt                  time.Time              `json:"authorized_at"`
	ExpiresAt                     time.Time              `json:"expires_at"`
	ApprovedBy                    string                 `json:"approved_by"`
	EvidenceRoot                  string                 `json:"evidence_root"`
	TelegramConfigurationIdentity string                 `json:"telegram_configuration_identity"`
	PaperCapitalMinor             int64                  `json:"paper_capital_minor"`
	Currency                      string                 `json:"currency"`
	PortfolioID                   string                 `json:"portfolio_id"`
	Strategy                      AuthorizedStrategy     `json:"strategy"`
	Artifacts                     AuthorizationArtifacts `json:"artifacts"`
	LiveTradingAuthorized         bool                   `json:"live_trading_authorized"`
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
	if value.SchemaVersion != AuthorizationSchemaVersion || !validCommit(value.ApplicationCommit) || value.Mode != "PAPER" ||
		(value.Scope != ScopeOperationsOnly && value.Scope != ScopeFullPipeline) || value.LiveTradingAuthorized ||
		value.AuthorizedAt.IsZero() || !value.ExpiresAt.After(value.AuthorizedAt) || value.ExpiresAt.Sub(value.AuthorizedAt) > 24*time.Hour ||
		strings.TrimSpace(value.ApprovedBy) == "" || strings.TrimSpace(value.EvidenceRoot) == "" || unsafeIdentity(value.EvidenceRoot) ||
		strings.TrimSpace(value.TelegramConfigurationIdentity) == "" || unsafeIdentity(value.TelegramConfigurationIdentity) ||
		value.PaperCapitalMinor != 100000000 || value.Currency != "INR" || value.PortfolioID == "" {
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
	if value.Scope == ScopeOperationsOnly {
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
	if bundle.Checksum != value.Artifacts.RuntimeBundle.Identity || bundle.Watchlist.Version != value.Artifacts.Watchlist.Identity ||
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
	if err != nil || portfolio.ID().String() != value.PortfolioID || portfolio.Hash().String() != value.Artifacts.Portfolio.Identity || portfolio.AllocationPolicy().Limits.TotalCapital.MinorUnits() != value.PaperCapitalMinor {
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
	if value.Artifacts.PrerequisiteDay0Gate != nil {
		var day0 GateReport
		if err := decodeStrictPath(resolveAuthorizationPath(base, value.Artifacts.PrerequisiteDay0Gate.Path), &day0); err != nil ||
			day0.SchemaVersion != Day0GateSchemaVersion || !day0.Passed || day0.LiveTradingAuthorized || day0.EvidenceSHA256 != value.Artifacts.PrerequisiteDay0Gate.Identity {
			return ErrInvalidRecord
		}
	}
	return nil
}

func authorizationArtifacts(value AuthorizationManifest) []AuthorizedArtifact {
	result := []AuthorizedArtifact{value.Artifacts.RuntimeBundle, value.Artifacts.Calendar, value.Artifacts.CalendarApproval, value.Artifacts.InstrumentMaster, value.Artifacts.Watchlist, value.Artifacts.Strategies, value.Artifacts.Portfolio, value.Artifacts.Risk}
	if value.Artifacts.PrerequisiteDay0Gate != nil {
		result = append(result, *value.Artifacts.PrerequisiteDay0Gate)
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
