package marketvalidationm1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"time"
)

const SchemaVersion = "market-validation-enablement-m1/v1"

var required = []string{"FORMAT", "TEST", "VET", "BUILD", "LINT", "RACE", "FOCUSED_RACE", "PHASE_REGRESSION", "READINESS", "AUTHORITY_SCANS"}

type Report struct {
	SchemaVersion        string          `json:"schema_version"`
	Commit               string          `json:"commit"`
	GeneratedAt          string          `json:"generated_at"`
	Mode                 string          `json:"mode"`
	ReadOnlyMarketData   bool            `json:"read_only_market_data"`
	LiveTradingEnabled   bool            `json:"live_trading_enabled"`
	ConfiguredStrategies int             `json:"configured_strategies"`
	Gates                map[string]bool `json:"gates"`
	FailureReasons       []string        `json:"failure_reasons"`
	Passed               bool            `json:"passed"`
	Checksum             string          `json:"checksum"`
}

func Run() Report {
	r := Report{SchemaVersion: SchemaVersion, Commit: os.Getenv("GITHUB_SHA"), GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), Mode: "PAPER", ReadOnlyMarketData: true, ConfiguredStrategies: 0, Gates: map[string]bool{}, FailureReasons: []string{}}
	for _, name := range required {
		passed := os.Getenv("TRADEEDGE_M1_GATE_"+name) == "passed"
		r.Gates[name] = passed
		if !passed {
			r.FailureReasons = append(r.FailureReasons, name+"_NOT_PROVEN")
		}
	}
	sort.Strings(r.FailureReasons)
	r.Passed = len(r.FailureReasons) == 0
	raw, _ := json.Marshal(r)
	sum := sha256.Sum256(raw)
	r.Checksum = hex.EncodeToString(sum[:])
	return r
}
