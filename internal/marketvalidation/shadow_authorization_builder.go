package marketvalidation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/bibhuyash/tradeedge/internal/adapters/marketdata/calendarfile"
	"github.com/bibhuyash/tradeedge/internal/config"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	portfolioconfig "github.com/bibhuyash/tradeedge/internal/portfolio/config"
	riskconfig "github.com/bibhuyash/tradeedge/internal/risk/config"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	"github.com/bibhuyash/tradeedge/internal/risk/rules"
)

type ShadowAuthorizationInputs struct {
	OutputPath             string
	ApplicationCommit      string
	TradingDate            string
	AuthorizedAt           time.Time
	ExpiresAt              time.Time
	RuntimeBundlePath      string
	CalendarPath           string
	CalendarApprovalPath   string
	InstrumentMasterPath   string
	WatchlistPath          string
	StrategiesPath         string
	PortfolioPath          string
	RiskPath               string
	QualificationNIFTYPath string
	QualificationBANKPath  string
	TelegramEvidencePath   string
	ZerodhaPreflightPath   string
}

// BuildShadowAuthorization derives every artifact checksum and identity from
// validated files. Operators never copy checksums or edit authorization JSON.
func BuildShadowAuthorization(input ShadowAuthorizationInputs) (AuthorizationManifest, error) {
	bundle, err := config.LoadRuntimeBundle(input.RuntimeBundlePath)
	if err != nil || bundle.Manifest.Mode != "SHADOW" {
		return AuthorizationManifest{}, ErrInvalidRecord
	}
	schedule, err := calendarfile.Load(input.CalendarPath)
	if err != nil {
		return AuthorizationManifest{}, err
	}
	master, _, err := instrumentmaster.LoadFile(input.InstrumentMasterPath)
	if err != nil {
		return AuthorizationManifest{}, err
	}
	portfolioRaw, err := os.ReadFile(input.PortfolioPath)
	if err != nil {
		return AuthorizationManifest{}, err
	}
	portfolio, err := portfolioconfig.Decode(portfolioRaw)
	if err != nil {
		return AuthorizationManifest{}, err
	}
	riskRaw, err := os.ReadFile(input.RiskPath)
	if err != nil {
		return AuthorizationManifest{}, err
	}
	descriptors := map[riskmodel.RiskRuleID]riskmodel.RiskRuleDescriptor{}
	for _, rule := range rules.ProductionCatalog() {
		descriptors[rule.Descriptor().ID] = rule.Descriptor()
	}
	risk, err := riskconfig.Decode(riskRaw, descriptors, portfolio.AllocationPolicy().Limits.ExposureGroups)
	if err != nil {
		return AuthorizationManifest{}, err
	}
	var approval CalendarApproval
	approvalRaw, err := os.ReadFile(input.CalendarApprovalPath)
	if err != nil || json.Unmarshal(approvalRaw, &approval) != nil || !approval.Approved {
		return AuthorizationManifest{}, ErrInvalidRecord
	}
	artifact := func(path, identity string) (AuthorizedArtifact, error) {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return AuthorizedArtifact{}, readErr
		}
		relative, relErr := filepath.Rel(filepath.Dir(input.OutputPath), path)
		if relErr != nil {
			return AuthorizedArtifact{}, relErr
		}
		sum := sha256.Sum256(raw)
		return AuthorizedArtifact{Path: filepath.ToSlash(relative), SHA256: hex.EncodeToString(sum[:]), Identity: identity}, nil
	}
	runtimeArtifact, err := artifact(input.RuntimeBundlePath, bundle.Checksum)
	if err != nil {
		return AuthorizationManifest{}, err
	}
	calendarArtifact, err := artifact(input.CalendarPath, string(schedule.Version()))
	if err != nil {
		return AuthorizationManifest{}, err
	}
	approvalArtifact, err := artifact(input.CalendarApprovalPath, approval.CalendarSHA256)
	if err != nil {
		return AuthorizationManifest{}, err
	}
	masterArtifact, err := artifact(input.InstrumentMasterPath, string(master.Version()))
	if err != nil {
		return AuthorizationManifest{}, err
	}
	watchlistArtifact, err := artifact(input.WatchlistPath, bundle.Watchlist.Version)
	if err != nil {
		return AuthorizationManifest{}, err
	}
	strategiesRaw, err := os.ReadFile(input.StrategiesPath)
	if err != nil {
		return AuthorizationManifest{}, err
	}
	strategySum := sha256.Sum256(strategiesRaw)
	strategyHash := hex.EncodeToString(strategySum[:])
	strategiesArtifact, err := artifact(input.StrategiesPath, "1")
	if err != nil {
		return AuthorizationManifest{}, err
	}
	portfolioArtifact, err := artifact(input.PortfolioPath, portfolio.Hash().String())
	if err != nil {
		return AuthorizationManifest{}, err
	}
	riskArtifact, err := artifact(input.RiskPath, risk.Hash().String())
	if err != nil {
		return AuthorizationManifest{}, err
	}
	niftyRaw, err := os.ReadFile(input.QualificationNIFTYPath)
	if err != nil {
		return AuthorizationManifest{}, err
	}
	niftySum := sha256.Sum256(niftyRaw)
	niftyIdentity := hex.EncodeToString(niftySum[:])
	niftyArtifact, err := artifact(input.QualificationNIFTYPath, niftyIdentity)
	if err != nil {
		return AuthorizationManifest{}, err
	}
	bankRaw, err := os.ReadFile(input.QualificationBANKPath)
	if err != nil {
		return AuthorizationManifest{}, err
	}
	bankSum := sha256.Sum256(bankRaw)
	bankIdentity := hex.EncodeToString(bankSum[:])
	bankArtifact, err := artifact(input.QualificationBANKPath, bankIdentity)
	if err != nil {
		return AuthorizationManifest{}, err
	}
	telegramRaw, err := os.ReadFile(input.TelegramEvidencePath)
	if err != nil {
		return AuthorizationManifest{}, err
	}
	telegramSum := sha256.Sum256(telegramRaw)
	telegramArtifact, err := artifact(input.TelegramEvidencePath, hex.EncodeToString(telegramSum[:]))
	if err != nil {
		return AuthorizationManifest{}, err
	}
	preflightRaw, err := os.ReadFile(input.ZerodhaPreflightPath)
	if err != nil {
		return AuthorizationManifest{}, err
	}
	preflightSum := sha256.Sum256(preflightRaw)
	preflightArtifact, err := artifact(input.ZerodhaPreflightPath, hex.EncodeToString(preflightSum[:]))
	if err != nil {
		return AuthorizationManifest{}, err
	}
	value := AuthorizationManifest{
		SchemaVersion: ShadowAuthorizationSchemaVersion, ApplicationCommit: input.ApplicationCommit, Mode: "SHADOW", Scope: ScopeQualificationOnly,
		TradingDate: input.TradingDate, AuthorizedAt: input.AuthorizedAt, ExpiresAt: input.ExpiresAt, ApprovedBy: "PHASE8_M4_AUTOMATED_PREPARATION",
		EvidenceRoot: filepath.ToSlash(filepath.Dir(input.OutputPath)), PaperCapitalMinor: 0, Currency: "INR", PortfolioID: portfolio.ID().String(),
		Strategy:                     AuthorizedStrategy{Name: "EMA_REFERENCE_V1", Version: "1", Classification: "REFERENCE_CANDIDATE", ConfigurationHash: strategyHash, CASPolicy: "CAS_RESTRICTED", Enabled: true},
		Artifacts:                    AuthorizationArtifacts{RuntimeBundle: runtimeArtifact, Calendar: calendarArtifact, CalendarApproval: approvalArtifact, InstrumentMaster: masterArtifact, Watchlist: watchlistArtifact, Strategies: strategiesArtifact, Portfolio: portfolioArtifact, Risk: riskArtifact, TelegramEvidence: telegramArtifact, ZerodhaPreflight: preflightArtifact, QualificationNIFTY: &niftyArtifact, QualificationBANKNIFTY: &bankArtifact},
		RealBrokerMutationProhibited: true, PaperExecutionProhibited: true, QualificationEnabled: true, ApprovedUnderlyings: []string{"BANKNIFTY", "NIFTY"},
	}
	return FinalizeAuthorization(input.OutputPath, value)
}
