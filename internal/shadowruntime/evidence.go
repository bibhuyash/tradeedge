package shadowruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

const RealMarketEvidenceSchemaVersion = "phase8-m4-real-market-shadow-session/v1"

type RealMarketEvidence struct {
	SchemaVersion         string             `json:"schema_version"`
	Checksum              string             `json:"checksum"`
	ApplicationCommit     string             `json:"application_commit"`
	TradingDate           string             `json:"trading_date"`
	Mode                  string             `json:"mode"`
	AuthorizationChecksum string             `json:"authorization_checksum"`
	RuntimeBundleChecksum string             `json:"runtime_bundle_checksum"`
	StartedAt             time.Time          `json:"started_at"`
	EndedAt               time.Time          `json:"ended_at"`
	Authorized            bool               `json:"authorized"`
	ZerodhaConnected      bool               `json:"zerodha_connected"`
	RealBrokerMutations   uint64             `json:"real_broker_mutations"`
	PaperMutations        uint64             `json:"paper_mutations"`
	Scorecards            []SessionScorecard `json:"scorecards"`
}

// FinalizeRealMarketEvidence accepts only a closed, explicitly authorized real
// SHADOW session. Engineering fixtures must use a different schema/path.
func FinalizeRealMarketEvidence(value RealMarketEvidence) (RealMarketEvidence, error) {
	value.SchemaVersion, value.Checksum, value.Mode = RealMarketEvidenceSchemaVersion, "", "SHADOW"
	if !value.Authorized || !value.ZerodhaConnected || value.RealBrokerMutations != 0 || value.PaperMutations != 0 ||
		(len(value.ApplicationCommit) != 40 && len(value.ApplicationCommit) != 64) || len(value.AuthorizationChecksum) != 64 ||
		len(value.RuntimeBundleChecksum) != 64 || value.StartedAt.IsZero() || !value.EndedAt.After(value.StartedAt) ||
		value.TradingDate != value.StartedAt.In(time.FixedZone("IST", 19800)).Format("2006-01-02") || len(value.Scorecards) != 2 {
		return RealMarketEvidence{}, ErrInvalid
	}
	seen := map[string]bool{}
	for _, card := range value.Scorecards {
		if card.TradingDate != value.TradingDate || card.EndedAt.IsZero() || card.Checksum == "" || card.Quality == SessionCollecting || seen[string(card.Underlying)] {
			return RealMarketEvidence{}, ErrInvalid
		}
		seen[string(card.Underlying)] = true
	}
	for _, digest := range []string{value.ApplicationCommit, value.AuthorizationChecksum, value.RuntimeBundleChecksum} {
		if _, err := hex.DecodeString(strings.ToLower(digest)); err != nil {
			return RealMarketEvidence{}, ErrInvalid
		}
	}
	sort.Slice(value.Scorecards, func(i, j int) bool { return value.Scorecards[i].Underlying < value.Scorecards[j].Underlying })
	raw, err := json.Marshal(value)
	if err != nil {
		return RealMarketEvidence{}, errors.Join(ErrInvalid, err)
	}
	sum := sha256.Sum256(raw)
	value.Checksum = hex.EncodeToString(sum[:])
	return value, nil
}
