package tradingruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

const CheckpointSchemaVersion = 1

type CheckpointHead struct {
	Subsystem string `json:"subsystem"`
	Revision  string `json:"revision"`
	Checksum  string `json:"checksum"`
}

type CheckpointManifest struct {
	SchemaVersion   int              `json:"schema_version"`
	Mode            Mode             `json:"mode"`
	CalendarVersion string           `json:"calendar_version"`
	Configuration   string           `json:"configuration_checksum"`
	Session         SessionState     `json:"session_state"`
	Regime          string           `json:"regime,omitempty"`
	Heads           []CheckpointHead `json:"heads"`
	CreatedAt       time.Time        `json:"created_at"`
	CleanShutdown   bool             `json:"clean_shutdown"`
	Checksum        string           `json:"checksum"`
}

func NewCheckpointManifest(value CheckpointManifest) (CheckpointManifest, error) {
	value.Checksum = ""
	if value.SchemaVersion != CheckpointSchemaVersion || value.Mode.Validate() != nil || value.CalendarVersion == "" || value.Configuration == "" || value.CreatedAt.IsZero() || len(value.Heads) == 0 {
		return CheckpointManifest{}, ErrCheckpointCorrupt
	}
	value.CreatedAt = value.CreatedAt.UTC()
	value.Heads = append([]CheckpointHead(nil), value.Heads...)
	sort.Slice(value.Heads, func(i, j int) bool { return value.Heads[i].Subsystem < value.Heads[j].Subsystem })
	for index, head := range value.Heads {
		if head.Subsystem == "" || head.Revision == "" || head.Checksum == "" || (index > 0 && value.Heads[index-1].Subsystem == head.Subsystem) {
			return CheckpointManifest{}, ErrCheckpointCorrupt
		}
	}
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	value.Checksum = hex.EncodeToString(sum[:])
	return value, nil
}

func (value CheckpointManifest) Verify() error {
	expected := value.Checksum
	rebuilt, err := NewCheckpointManifest(value)
	if err != nil || rebuilt.Checksum != expected {
		return ErrCheckpointCorrupt
	}
	return nil
}

func (value CheckpointManifest) CanonicalJSON() []byte { raw, _ := json.Marshal(value); return raw }
