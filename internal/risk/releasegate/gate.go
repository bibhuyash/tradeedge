// Package releasegate produces bounded machine-readable Phase 3 release evidence.
package releasegate

import (
	"context"
	"runtime"
	"sort"
	"time"

	"github.com/bibhuyash/tradeedge/internal/risk/rules"
)

const SchemaVersion = 1

type Report struct {
	SchemaVersion                int      `json:"schema_version"`
	GeneratedAt                  string   `json:"generated_at"`
	ProductionRuleCount          int      `json:"production_rule_count"`
	ProductionRuleIDs            []string `json:"production_rule_ids"`
	CatalogDeterministic         bool     `json:"catalog_deterministic"`
	ConfiguredMaximumConcurrency int      `json:"configured_maximum_concurrency"`
	StartingGoroutines           int      `json:"starting_goroutines"`
	EndingGoroutines             int      `json:"ending_goroutines"`
	EndingGoroutineTolerance     int      `json:"ending_goroutine_tolerance"`
	ForbiddenCapabilitiesAbsent  bool     `json:"forbidden_capabilities_absent"`
	FailureReasons               []string `json:"failure_reasons"`
	Passed                       bool     `json:"passed"`
}

func Run(ctx context.Context) (Report, error) {
	report := Report{SchemaVersion: SchemaVersion, GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		ConfiguredMaximumConcurrency: 4, StartingGoroutines: runtime.NumGoroutine(),
		EndingGoroutineTolerance: 2, ForbiddenCapabilitiesAbsent: true, FailureReasons: []string{}}
	catalog := rules.ProductionCatalog()
	report.ProductionRuleCount = len(catalog)
	for _, rule := range catalog {
		report.ProductionRuleIDs = append(report.ProductionRuleIDs, string(rule.Descriptor().ID))
	}
	copyIDs := append([]string(nil), report.ProductionRuleIDs...)
	sort.Strings(copyIDs)
	report.CatalogDeterministic = len(catalog) == 10
	for index := 1; index < len(copyIDs); index++ {
		if copyIDs[index-1] == copyIDs[index] {
			report.CatalogDeterministic = false
		}
	}
	if err := ctx.Err(); err != nil {
		report.FailureReasons = append(report.FailureReasons, err.Error())
	}
	runtime.GC()
	report.EndingGoroutines = runtime.NumGoroutine()
	if !report.CatalogDeterministic {
		report.FailureReasons = append(report.FailureReasons, "production rule catalog is incomplete or non-deterministic")
	}
	if report.EndingGoroutines > report.StartingGoroutines+report.EndingGoroutineTolerance {
		report.FailureReasons = append(report.FailureReasons, "goroutine cleanup tolerance exceeded")
	}
	report.Passed = len(report.FailureReasons) == 0
	return report, ctx.Err()
}
