package marketvalidationm2

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"time"
)

const SchemaVersion = "market-validation-enablement-m2/v1"

var required = []string{"FORMAT", "TEST", "VET", "BUILD", "LINT", "RACE", "M1_REGRESSION", "PHASE_REGRESSION", "MAPPING", "EXPIRED_CONTRACT", "MANIFEST_MISMATCH", "ZERO_STRATEGY", "DAY0_GATE", "RISK_CONFIGURATION", "AUTHORITY_SCANS"}

type Report struct {
	SchemaVersion      string          `json:"schema_version"`
	Commit             string          `json:"commit"`
	GeneratedAt        string          `json:"generated_at"`
	Mode               string          `json:"mode"`
	Strategy           string          `json:"strategy"`
	PaperCapitalMinor  int64           `json:"paper_capital_minor"`
	Day0Ready          bool            `json:"day0_ready"`
	Day1Ready          bool            `json:"day1_ready"`
	LiveTradingEnabled bool            `json:"live_trading_enabled"`
	Gates              map[string]bool `json:"gates"`
	FailureReasons     []string        `json:"failure_reasons"`
	Passed             bool            `json:"passed"`
	Checksum           string          `json:"checksum"`
}

func Run() Report {
	r := Report{SchemaVersion: SchemaVersion, Commit: os.Getenv("GITHUB_SHA"), GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), Mode: "PAPER", Strategy: "NONE", PaperCapitalMinor: 100000000, Day0Ready: false, Day1Ready: false, LiveTradingEnabled: false, Gates: map[string]bool{}, FailureReasons: []string{}}
	for _, name := range required {
		passed := os.Getenv("TRADEEDGE_M2_GATE_"+name) == "passed"
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
