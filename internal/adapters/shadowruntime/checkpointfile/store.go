// Package checkpointfile persists the complete Phase 8 M4 live SHADOW state
// through the existing atomic generation store.
package checkpointfile

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	platform "github.com/bibhuyash/tradeedge/internal/platform/checkpointfile"
	"github.com/bibhuyash/tradeedge/internal/shadowruntime"
)

const ComponentName = "live-shadow-runtime"

var ErrInvalid = errors.New("invalid live shadow checkpoint")

type Store struct{ base *platform.Store }

func New(root string) (*Store, error) {
	base, err := platform.New(root)
	if err != nil {
		return nil, err
	}
	return &Store{base: base}, nil
}

func (s *Store) Publish(ctx context.Context, snapshot shadowruntime.Snapshot, expected uint64, calendarVersion, configurationChecksum string, at time.Time, clean bool) (platform.Generation, error) {
	if snapshot.SchemaVersion != shadowruntime.SchemaVersion || snapshot.Revision == 0 || snapshot.Checksum == "" || at.IsZero() || calendarVersion == "" || configurationChecksum == "" {
		return platform.Generation{}, ErrInvalid
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return platform.Generation{}, err
	}
	return s.base.Publish(ctx, platform.Generation{
		SchemaVersion: platform.SchemaVersion, Sequence: expected + 1, Mode: "SHADOW",
		CalendarVersion: calendarVersion, ConfigurationChecksum: configurationChecksum,
		CreatedAt: at.UTC(), CleanShutdown: clean,
		Components: []platform.Component{{Name: ComponentName, Revision: strconv.FormatUint(snapshot.Revision, 10), Data: raw}},
	}, expected)
}

func (s *Store) Load(ctx context.Context) (shadowruntime.Snapshot, platform.Generation, error) {
	generation, err := s.base.Load(ctx)
	if err != nil {
		return shadowruntime.Snapshot{}, platform.Generation{}, err
	}
	if generation.Mode != "SHADOW" || len(generation.Components) != 1 || generation.Components[0].Name != ComponentName {
		return shadowruntime.Snapshot{}, platform.Generation{}, ErrInvalid
	}
	var snapshot shadowruntime.Snapshot
	if json.Unmarshal(generation.Components[0].Data, &snapshot) != nil || snapshot.SchemaVersion != shadowruntime.SchemaVersion || snapshot.Checksum == "" || strconv.FormatUint(snapshot.Revision, 10) != generation.Components[0].Revision {
		return shadowruntime.Snapshot{}, platform.Generation{}, ErrInvalid
	}
	return snapshot, generation, nil
}
