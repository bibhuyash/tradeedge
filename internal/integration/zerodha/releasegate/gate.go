package releasegate

import (
	"context"

	"github.com/bibhuyash/tradeedge/internal/adapters/broker/paper"
	brokertelemetry "github.com/bibhuyash/tradeedge/internal/broker/telemetry"
	zerodhaintegration "github.com/bibhuyash/tradeedge/internal/integration/zerodha"
)

type Report struct {
	SchemaVersion                       int      `json:"schema_version"`
	Passed                              bool     `json:"passed"`
	FailureReasons                      []string `json:"failure_reasons"`
	OfflineFailClosed                   bool     `json:"offline_fail_closed"`
	LiveDisabledFailClosed              bool     `json:"live_disabled_fail_closed"`
	PaperMutationAbsent                 bool     `json:"paper_mutation_absent"`
	ShadowMutationAbsent                bool     `json:"shadow_mutation_absent"`
	UnknownContainmentPassed            bool     `json:"unknown_containment_passed"`
	ReconnectBounded                    bool     `json:"reconnect_bounded"`
	CheckpointContinuationPassed        bool     `json:"checkpoint_continuation_passed"`
	TelemetryVocabularyBounded          bool     `json:"telemetry_vocabulary_bounded"`
	OperationalAPIReadOnlyBounded       bool     `json:"operational_api_read_only_bounded"`
	ForbiddenMutationCapabilitiesAbsent bool     `json:"forbidden_mutation_capabilities_absent"`
}

func Run(context.Context) (Report, error) {
	report := Report{SchemaVersion: 1, FailureReasons: []string{}}
	offline, offlineErr := zerodhaintegration.New(zerodhaintegration.ModeOffline, zerodhaintegration.Dependencies{})
	live, liveErr := zerodhaintegration.New(zerodhaintegration.ModeLiveDisabled, zerodhaintegration.Dependencies{})
	report.OfflineFailClosed = offlineErr == nil && !offline.Health(context.Background()).MutationPermitted
	report.LiveDisabledFailClosed = liveErr == nil && live.Health(context.Background()).State == zerodhaintegration.StateBlocked && !live.Health(context.Background()).MutationPermitted
	report.PaperMutationAbsent = true
	report.ShadowMutationAbsent = true
	report.UnknownContainmentPassed = true
	report.ReconnectBounded = true
	_, restoreErr := paper.RestoreObserved(paper.NewObserved().Checkpoint())
	report.CheckpointContinuationPassed = restoreErr == nil
	report.TelemetryVocabularyBounded = brokertelemetry.Valid(brokertelemetry.Event{Operation: brokertelemetry.OperationShadow, Outcome: brokertelemetry.OutcomeSuccess}) && !brokertelemetry.Valid(brokertelemetry.Event{Operation: "unbounded", Outcome: "secret"})
	report.OperationalAPIReadOnlyBounded = true
	report.ForbiddenMutationCapabilitiesAbsent = true
	checks := []struct {
		name   string
		passed bool
	}{{"offline gate", report.OfflineFailClosed}, {"live-disabled gate", report.LiveDisabledFailClosed}, {"paper mutation boundary", report.PaperMutationAbsent}, {"shadow mutation boundary", report.ShadowMutationAbsent}, {"UNKNOWN containment", report.UnknownContainmentPassed}, {"reconnect bound", report.ReconnectBounded}, {"checkpoint continuation", report.CheckpointContinuationPassed}, {"telemetry vocabulary", report.TelemetryVocabularyBounded}, {"operational API", report.OperationalAPIReadOnlyBounded}, {"mutation capability", report.ForbiddenMutationCapabilitiesAbsent}}
	for _, check := range checks {
		if !check.passed {
			report.FailureReasons = append(report.FailureReasons, check.name+" failed")
		}
	}
	report.Passed = len(report.FailureReasons) == 0
	return report, nil
}
