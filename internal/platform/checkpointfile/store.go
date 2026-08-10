// Package checkpointfile publishes checksummed, atomic PAPER runtime state
// generations. It is deliberately filesystem-only and never accepts secrets.
package checkpointfile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotFound  = errors.New("runtime checkpoint not found")
	ErrCorrupt   = errors.New("runtime checkpoint corrupt")
	ErrConflict  = errors.New("runtime checkpoint publication conflict")
	ErrSensitive = errors.New("runtime checkpoint contains forbidden sensitive material")
)

const SchemaVersion = "tradeedge-paper-runtime-checkpoint/v1"

type Component struct {
	Name     string          `json:"name"`
	Revision string          `json:"revision"`
	Checksum string          `json:"checksum"`
	Data     json.RawMessage `json:"data"`
}

type Generation struct {
	SchemaVersion         string      `json:"schema_version"`
	Sequence              uint64      `json:"sequence"`
	Mode                  string      `json:"mode"`
	CalendarVersion       string      `json:"calendar_version"`
	ConfigurationChecksum string      `json:"configuration_checksum"`
	CreatedAt             time.Time   `json:"created_at"`
	CleanShutdown         bool        `json:"clean_shutdown"`
	Components            []Component `json:"components"`
	Checksum              string      `json:"checksum"`
}

func NewGeneration(value Generation) (Generation, error) {
	value.Checksum = ""
	value.CreatedAt = value.CreatedAt.UTC()
	if value.SchemaVersion != SchemaVersion || value.Sequence == 0 || value.Mode != "PAPER" || value.CalendarVersion == "" || !digest(value.ConfigurationChecksum) || value.CreatedAt.IsZero() || len(value.Components) == 0 {
		return Generation{}, ErrCorrupt
	}
	value.Components = append([]Component(nil), value.Components...)
	sort.Slice(value.Components, func(i, j int) bool { return value.Components[i].Name < value.Components[j].Name })
	for index := range value.Components {
		component := &value.Components[index]
		if component.Name == "" || component.Revision == "" || len(component.Data) == 0 || !json.Valid(component.Data) || sensitive(component.Name) || sensitive(string(component.Data)) || (index > 0 && value.Components[index-1].Name == component.Name) {
			return Generation{}, ErrSensitive
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, component.Data); err != nil {
			return Generation{}, ErrCorrupt
		}
		component.Data = append(json.RawMessage(nil), compact.Bytes()...)
		sum := sha256.Sum256(component.Data)
		computed := hex.EncodeToString(sum[:])
		if component.Checksum != "" && component.Checksum != computed {
			return Generation{}, ErrCorrupt
		}
		component.Checksum = computed
		component.Data = append(json.RawMessage(nil), component.Data...)
	}
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	value.Checksum = hex.EncodeToString(sum[:])
	return value, nil
}

func (value Generation) Verify() error {
	expected := value.Checksum
	rebuilt, err := NewGeneration(value)
	if err != nil || rebuilt.Checksum != expected {
		return ErrCorrupt
	}
	return nil
}

type Store struct {
	root string
	mu   sync.Mutex
}

func New(root string) (*Store, error) {
	clean, err := filepath.Abs(filepath.Clean(root))
	if err != nil || strings.TrimSpace(root) == "" || clean == filepath.VolumeName(clean)+string(filepath.Separator) || sensitive(clean) {
		return nil, ErrCorrupt
	}
	return &Store{root: clean}, nil
}

func (s *Store) Publish(ctx context.Context, value Generation, expected uint64) (Generation, error) {
	if err := ctx.Err(); err != nil {
		return Generation{}, err
	}
	validated, err := NewGeneration(value)
	if err != nil {
		return Generation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, currentErr := s.loadLocked()
	actual := uint64(0)
	if currentErr == nil {
		actual = current.Sequence
	} else if !errors.Is(currentErr, ErrNotFound) {
		return Generation{}, currentErr
	}
	if actual != expected || validated.Sequence != expected+1 {
		return Generation{}, ErrConflict
	}
	if err := os.MkdirAll(filepath.Join(s.root, "generations"), 0o750); err != nil {
		return Generation{}, err
	}
	name := fmt.Sprintf("%020d-%s", validated.Sequence, validated.Checksum[:16])
	final := filepath.Join(s.root, "generations", name)
	if _, err := os.Stat(final); err == nil {
		existing, loadErr := loadGeneration(final)
		if loadErr == nil && existing.Checksum == validated.Checksum {
			return existing, s.publishCurrent(name)
		}
		return Generation{}, ErrConflict
	}
	temporary, err := os.MkdirTemp(filepath.Join(s.root, "generations"), ".pending-")
	if err != nil {
		return Generation{}, err
	}
	keep := true
	defer func() {
		if keep {
			_ = os.WriteFile(filepath.Join(temporary, "QUARANTINED"), []byte("incomplete checkpoint generation\n"), 0o640)
		}
	}()
	raw, _ := json.MarshalIndent(validated, "", "  ")
	raw = append(raw, '\n')
	if err := writeSync(filepath.Join(temporary, "manifest.json"), raw); err != nil {
		return Generation{}, err
	}
	if err := os.Rename(temporary, final); err != nil {
		return Generation{}, err
	}
	keep = false
	if err := s.publishCurrent(name); err != nil {
		return Generation{}, err
	}
	return validated, nil
}

func (s *Store) Load(ctx context.Context) (Generation, error) {
	if err := ctx.Err(); err != nil {
		return Generation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() (Generation, error) {
	raw, err := os.ReadFile(filepath.Join(s.root, "CURRENT"))
	if errors.Is(err, os.ErrNotExist) {
		return Generation{}, ErrNotFound
	}
	if err != nil {
		return Generation{}, err
	}
	name := strings.TrimSpace(string(raw))
	if name == "" || filepath.Base(name) != name || strings.Contains(name, "..") {
		return Generation{}, ErrCorrupt
	}
	return loadGeneration(filepath.Join(s.root, "generations", name))
}

func loadGeneration(path string) (Generation, error) {
	raw, err := os.ReadFile(filepath.Join(path, "manifest.json"))
	if err != nil {
		return Generation{}, ErrCorrupt
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value Generation
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF || value.Verify() != nil {
		return Generation{}, ErrCorrupt
	}
	return value, nil
}

func (s *Store) publishCurrent(name string) error {
	if filepath.Base(name) != name {
		return ErrCorrupt
	}
	if err := os.MkdirAll(s.root, 0o750); err != nil {
		return err
	}
	temporary := filepath.Join(s.root, "CURRENT.tmp-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := writeSync(temporary, []byte(name+"\n")); err != nil {
		return err
	}
	if err := os.Rename(temporary, filepath.Join(s.root, "CURRENT")); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func writeSync(path string, raw []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	return errors.Join(err, closeErr)
}

func sensitive(value string) bool {
	lower := strings.ToLower(value)
	for _, forbidden := range []string{"api_secret", "access_token", "request_token", "bot_token", "credential", "authorization"} {
		if strings.Contains(lower, forbidden) {
			return true
		}
	}
	return false
}

func digest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
