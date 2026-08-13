// Package checkpointfile persists qualification snapshots inside the existing
// atomic, checksummed runtime generation mechanism.
package checkpointfile

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/bibhuyash/tradeedge/internal/platform/checkpointfile"
	"github.com/bibhuyash/tradeedge/internal/qualification"
)

var ErrInvalid = errors.New("invalid qualification checkpoint")

type Store struct{ base *checkpointfile.Store }

func New(root string) (*Store, error) {
	base, err := checkpointfile.New(root)
	if err != nil {
		return nil, err
	}
	return &Store{base}, nil
}

func (s *Store) Publish(ctx context.Context, snapshot qualification.Snapshot, expected uint64, calendarVersion, configurationChecksum string, at time.Time, clean bool) (checkpointfile.Generation, error) {
	if snapshot.Verify() != nil || snapshot.Revision == 0 || at.IsZero() {
		return checkpointfile.Generation{}, ErrInvalid
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return checkpointfile.Generation{}, err
	}
	value := checkpointfile.Generation{SchemaVersion: checkpointfile.SchemaVersion, Sequence: expected + 1, Mode: "SHADOW", CalendarVersion: calendarVersion, ConfigurationChecksum: configurationChecksum, CreatedAt: at.UTC(), CleanShutdown: clean, Components: []checkpointfile.Component{{Name: "shadow-qualification", Revision: strconv.FormatUint(snapshot.Revision, 10), Data: raw}}}
	return s.base.Publish(ctx, value, expected)
}

func (s *Store) Load(ctx context.Context) (qualification.Snapshot, checkpointfile.Generation, error) {
	generation, err := s.base.Load(ctx)
	if err != nil {
		return qualification.Snapshot{}, checkpointfile.Generation{}, err
	}
	if generation.Mode != "SHADOW" || len(generation.Components) != 1 || generation.Components[0].Name != "shadow-qualification" {
		return qualification.Snapshot{}, checkpointfile.Generation{}, ErrInvalid
	}
	var snapshot qualification.Snapshot
	if json.Unmarshal(generation.Components[0].Data, &snapshot) != nil || snapshot.Verify() != nil || strconv.FormatUint(snapshot.Revision, 10) != generation.Components[0].Revision {
		return qualification.Snapshot{}, checkpointfile.Generation{}, ErrInvalid
	}
	return snapshot, generation, nil
}
