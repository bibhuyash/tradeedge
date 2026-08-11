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
	"sort"
	"strings"
	"time"
)

const (
	Day0ClosureSchemaVersion     = "market-validation-day0-closure/v1"
	Day0AttestationSchemaVersion = "market-validation-day0-runtime-attestation/v1"
)

type Day0ClosureClassification string

const (
	Day0PartialSessionPass Day0ClosureClassification = "PARTIAL_SESSION_PASS"
	Day0SessionPass        Day0ClosureClassification = "SESSION_PASS"
	Day0SessionFail        Day0ClosureClassification = "SESSION_FAIL"
)

type Day0ClosureDraft struct {
	ApplicationCommit string              `json:"application_commit"`
	CIIdentity        string              `json:"ci_identity"`
	TradingDate       string              `json:"trading_date"`
	Mode              string              `json:"mode"`
	Scope             Scope               `json:"scope"`
	FullSession       bool                `json:"full_session"`
	Evidence          []EvidenceReference `json:"evidence"`
	Limitations       []string            `json:"limitations"`
	KnownIncidents    []string            `json:"known_incidents"`
	ResolvedIssues    []string            `json:"resolved_issues"`
	UnresolvedIssues  []string            `json:"unresolved_issues"`
}

type Day0RuntimeAttestation struct {
	SchemaVersion         string    `json:"schema_version"`
	ApplicationCommit     string    `json:"application_commit"`
	TradingDate           string    `json:"trading_date"`
	Mode                  string    `json:"mode"`
	CollectedAt           time.Time `json:"collected_at"`
	SessionStartedAt      time.Time `json:"session_started_at"`
	SessionEndedAt        time.Time `json:"session_ended_at"`
	StartupPass           bool      `json:"startup_pass"`
	RuntimeReady          bool      `json:"runtime_ready"`
	MarketDataReady       bool      `json:"market_data_ready"`
	RequiredInstruments   int       `json:"required_instruments"`
	CoveredInstruments    int       `json:"covered_instruments"`
	SessionClosed         bool      `json:"session_closed"`
	SessionClosedReason   string    `json:"session_closed_reason"`
	NormalTradingObserved bool      `json:"normal_trading_observed"`
	EODCloseCompleted     bool      `json:"eod_close_completed"`
	CheckpointGenerations int       `json:"checkpoint_generations"`
	OperatorSocketMode    string    `json:"operator_socket_mode"`
	ShutdownPass          bool      `json:"shutdown_pass"`
	ContainerExitCode     int       `json:"container_exit_code"`
	Strategies            int64     `json:"strategies"`
	Proposals             int64     `json:"proposals"`
	Orders                int64     `json:"orders"`
	Fills                 int64     `json:"fills"`
	RealBrokerMutations   int64     `json:"real_broker_mutations"`
}

type Day0Closure struct {
	SchemaVersion         string                    `json:"schema_version"`
	Checksum              string                    `json:"checksum"`
	ApplicationCommit     string                    `json:"application_commit"`
	CIIdentity            string                    `json:"ci_identity"`
	TradingDate           string                    `json:"trading_date"`
	Mode                  string                    `json:"mode"`
	Scope                 Scope                     `json:"scope"`
	Classification        Day0ClosureClassification `json:"classification"`
	AuthorizationChecksum string                    `json:"authorization_checksum"`
	SessionStartedAt      time.Time                 `json:"session_started_at"`
	SessionEndedAt        time.Time                 `json:"session_ended_at"`
	RuntimeReady          bool                      `json:"runtime_ready"`
	MarketDataReady       bool                      `json:"market_data_ready"`
	RequiredInstruments   int                       `json:"required_instruments"`
	CoveredInstruments    int                       `json:"covered_instruments"`
	EODCloseCompleted     bool                      `json:"eod_close_completed"`
	CleanCheckpoint       bool                      `json:"clean_checkpoint"`
	ContainerExitCode     int                       `json:"container_exit_code"`
	Strategies            int64                     `json:"strategies"`
	Proposals             int64                     `json:"proposals"`
	Orders                int64                     `json:"orders"`
	Fills                 int64                     `json:"fills"`
	RealBrokerMutations   int64                     `json:"real_broker_mutations"`
	EvidenceBasis         string                    `json:"evidence_basis"`
	Evidence              []EvidenceReference       `json:"evidence"`
	Limitations           []string                  `json:"limitations"`
	KnownIncidents        []string                  `json:"known_incidents"`
	ResolvedIssues        []string                  `json:"resolved_issues"`
	UnresolvedIssues      []string                  `json:"unresolved_issues"`
	LiveTradingAuthorized bool                      `json:"live_trading_authorized"`
}

func FinalizeDay0Closure(outputPath string, draft Day0ClosureDraft) (Day0Closure, error) {
	if !validCommit(draft.ApplicationCommit) || strings.TrimSpace(draft.CIIdentity) == "" || draft.Mode != "PAPER" || draft.Scope != ScopeOperationsOnly || len(draft.Evidence) != 7 || len(draft.KnownIncidents) == 0 || len(draft.ResolvedIssues) == 0 {
		return Day0Closure{}, ErrInvalidRecord
	}
	if _, err := time.Parse("2006-01-02", draft.TradingDate); err != nil {
		return Day0Closure{}, ErrInvalidRecord
	}
	base := filepath.Dir(outputPath)
	byKind := make(map[string]EvidenceReference, len(draft.Evidence))
	for _, reference := range draft.Evidence {
		if _, duplicate := byKind[reference.Kind]; duplicate || verifyClosureReference(base, reference) != nil {
			return Day0Closure{}, ErrInvalidRecord
		}
		byKind[reference.Kind] = reference
	}
	for _, kind := range []string{"authorization", "zerodha_preflight", "telegram", "runtime_attestation", "container_log", "runtime_checkpoint", "operator_controls"} {
		if _, found := byKind[kind]; !found {
			return Day0Closure{}, ErrInvalidRecord
		}
	}
	authorizationPath := filepath.Join(base, filepath.Clean(byKind["authorization"].Path))
	authorization, err := LoadAuthorization(authorizationPath)
	if err != nil || authorization.ApplicationCommit != draft.ApplicationCommit || authorization.TradingDate != draft.TradingDate || authorization.Mode != draft.Mode || authorization.Scope != draft.Scope ||
		!strings.EqualFold(authorization.Artifacts.ZerodhaPreflight.SHA256, byKind["zerodha_preflight"].SHA256) || !strings.EqualFold(authorization.Artifacts.TelegramEvidence.SHA256, byKind["telegram"].SHA256) {
		return Day0Closure{}, ErrInvalidRecord
	}
	var attestation Day0RuntimeAttestation
	if decodeStrictPath(filepath.Join(base, filepath.Clean(byKind["runtime_attestation"].Path)), &attestation) != nil ||
		attestation.SchemaVersion != Day0AttestationSchemaVersion || attestation.ApplicationCommit != draft.ApplicationCommit || attestation.TradingDate != draft.TradingDate || attestation.Mode != draft.Mode ||
		attestation.CollectedAt.IsZero() || attestation.SessionStartedAt.IsZero() || !attestation.SessionEndedAt.After(attestation.SessionStartedAt) {
		return Day0Closure{}, ErrInvalidRecord
	}
	cleanCheckpoint, eodCompleted := verifyClosureState(base, byKind, authorization.Artifacts.RuntimeBundle.Identity)
	classification := classifyDay0Attestation(attestation, cleanCheckpoint, eodCompleted, draft.FullSession)
	if classification == Day0PartialSessionPass && len(draft.Limitations) == 0 {
		return Day0Closure{}, ErrInvalidRecord
	}
	result := Day0Closure{SchemaVersion: Day0ClosureSchemaVersion, ApplicationCommit: draft.ApplicationCommit, CIIdentity: draft.CIIdentity, TradingDate: draft.TradingDate, Mode: draft.Mode, Scope: draft.Scope,
		Classification: classification, AuthorizationChecksum: authorization.Checksum, SessionStartedAt: attestation.SessionStartedAt.UTC(), SessionEndedAt: attestation.SessionEndedAt.UTC(),
		RuntimeReady: attestation.RuntimeReady, MarketDataReady: attestation.MarketDataReady, RequiredInstruments: attestation.RequiredInstruments, CoveredInstruments: attestation.CoveredInstruments,
		EODCloseCompleted: attestation.EODCloseCompleted, CleanCheckpoint: cleanCheckpoint, ContainerExitCode: attestation.ContainerExitCode, Strategies: attestation.Strategies,
		Proposals: attestation.Proposals, Orders: attestation.Orders, Fills: attestation.Fills, RealBrokerMutations: attestation.RealBrokerMutations,
		EvidenceBasis: "CHECKSUM_BOUND_FILES_AND_OPERATOR_ATTESTATION", Evidence: append([]EvidenceReference(nil), draft.Evidence...), Limitations: append([]string(nil), draft.Limitations...),
		KnownIncidents: append([]string(nil), draft.KnownIncidents...), ResolvedIssues: append([]string(nil), draft.ResolvedIssues...), UnresolvedIssues: append([]string(nil), draft.UnresolvedIssues...), LiveTradingAuthorized: false}
	sort.Slice(result.Evidence, func(i, j int) bool { return result.Evidence[i].Kind < result.Evidence[j].Kind })
	sort.Strings(result.Limitations)
	sort.Strings(result.KnownIncidents)
	sort.Strings(result.ResolvedIssues)
	sort.Strings(result.UnresolvedIssues)
	raw, _ := json.Marshal(result)
	sum := sha256.Sum256(raw)
	result.Checksum = hex.EncodeToString(sum[:])
	return result, nil
}

func classifyDay0Attestation(value Day0RuntimeAttestation, cleanCheckpoint, eodCompleted, fullSession bool) Day0ClosureClassification {
	safe := value.StartupPass && value.RuntimeReady && value.MarketDataReady && value.RequiredInstruments == 2 && value.CoveredInstruments == 2 &&
		value.NormalTradingObserved && value.SessionClosed && value.SessionClosedReason == "AFTER_CLOSE" && value.EODCloseCompleted && value.CheckpointGenerations >= 3 && value.OperatorSocketMode == "0600" && value.ShutdownPass && value.ContainerExitCode == 0 && cleanCheckpoint && eodCompleted &&
		value.Strategies == 0 && value.Proposals == 0 && value.Orders == 0 && value.Fills == 0 && value.RealBrokerMutations == 0
	if !safe {
		return Day0SessionFail
	}
	if fullSession {
		return Day0SessionPass
	}
	return Day0PartialSessionPass
}

func verifyClosureReference(base string, reference EvidenceReference) error {
	clean := filepath.Clean(reference.Path)
	if strings.TrimSpace(reference.Kind) == "" || filepath.IsAbs(reference.Path) || clean == "." || !validDigest(strings.ToLower(reference.SHA256)) || unsafeIdentity(reference.Path) {
		return ErrInvalidRecord
	}
	root, _ := filepath.Abs(filepath.Dir(base))
	candidate, _ := filepath.Abs(filepath.Join(base, clean))
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ErrInvalidRecord
	}
	raw, err := os.ReadFile(candidate)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != strings.ToLower(reference.SHA256) {
		return ErrInvalidRecord
	}
	return nil
}

func verifyClosureState(base string, evidence map[string]EvidenceReference, configurationChecksum string) (bool, bool) {
	var checkpoint struct {
		SchemaVersion         string    `json:"schema_version"`
		Sequence              uint64    `json:"sequence"`
		Mode                  string    `json:"mode"`
		CalendarVersion       string    `json:"calendar_version"`
		ConfigurationChecksum string    `json:"configuration_checksum"`
		CreatedAt             time.Time `json:"created_at"`
		CleanShutdown         bool      `json:"clean_shutdown"`
		Components            []struct {
			Name, Revision, Checksum string
			Data                     struct {
				Strategies, Orders, Fills int64
				State                     string
				Restored                  bool
			} `json:"data"`
		} `json:"components"`
		Checksum string `json:"checksum"`
	}
	var controls struct {
		SchemaVersion      string            `json:"schema_version"`
		Revision           uint64            `json:"revision"`
		NewExposureBlocked bool              `json:"new_exposure_blocked"`
		EODState           string            `json:"eod_state"`
		Commands           []json.RawMessage `json:"commands"`
		Checksum           string            `json:"checksum"`
	}
	checkpointOK := decodeStrictPath(filepath.Join(base, filepath.Clean(evidence["runtime_checkpoint"].Path)), &checkpoint) == nil && checkpoint.SchemaVersion == "tradeedge-paper-runtime-checkpoint/v1" && checkpoint.Mode == "PAPER" && checkpoint.ConfigurationChecksum == configurationChecksum && validDigest(checkpoint.Checksum) && checkpoint.CleanShutdown && len(checkpoint.Components) == 1 && checkpoint.Components[0].Data.Strategies == 0 && checkpoint.Components[0].Data.Orders == 0 && checkpoint.Components[0].Data.Fills == 0
	controlOK := decodeStrictPath(filepath.Join(base, filepath.Clean(evidence["operator_controls"].Path)), &controls) == nil && controls.SchemaVersion == "tradeedge-operator-controls/v1" && validDigest(controls.Checksum) && controls.NewExposureBlocked && controls.EODState == "COMPLETED"
	return checkpointOK, controlOK
}

func DecodeDay0ClosureDraft(raw []byte) (Day0ClosureDraft, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value Day0ClosureDraft
	if decoder.Decode(&value) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return Day0ClosureDraft{}, ErrInvalidRecord
	}
	return value, nil
}
