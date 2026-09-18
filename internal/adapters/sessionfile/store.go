package sessionfile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/session"
)

const SchemaVersion = "tradeedge-zerodha-session/v1"

type Store struct{ path string }

type document struct {
	SchemaVersion   string    `json:"schema_version"`
	Provider        string    `json:"provider"`
	AccessToken     string    `json:"access_token"`
	ExpiresAt       time.Time `json:"expires_at"`
	AuthenticatedAt time.Time `json:"authenticated_at"`
}

func New(path string) (*Store, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || filepath.Base(path) == "." || filepath.Ext(path) == "" {
		return nil, session.ErrInvalid
	}
	return &Store{path: path}, nil
}

func (s *Store) Load(ctx context.Context) (session.Record, error) {
	if err := ctx.Err(); err != nil {
		return session.Record{}, err
	}
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return session.Record{}, session.ErrNotFound
	}
	if err != nil {
		return session.Record{}, errors.Join(session.ErrCorrupt, errors.New("load session store"))
	}
	defer file.Close()
	if info, statErr := file.Stat(); statErr != nil || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return session.Record{}, session.ErrCorrupt
	}
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	var value document
	if err = decoder.Decode(&value); err != nil {
		return session.Record{}, session.ErrCorrupt
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return session.Record{}, session.ErrCorrupt
	}
	if value.SchemaVersion != SchemaVersion || value.Provider != session.ProviderZerodha ||
		strings.TrimSpace(value.AccessToken) == "" || strings.ContainsAny(value.AccessToken, "\r\n\x00") ||
		value.ExpiresAt.IsZero() || value.AuthenticatedAt.IsZero() || value.AuthenticatedAt.After(value.ExpiresAt) {
		return session.Record{}, session.ErrCorrupt
	}
	return session.Record{Provider: value.Provider, AccessToken: value.AccessToken, ExpiresAt: value.ExpiresAt.UTC(), AuthenticatedAt: value.AuthenticatedAt.UTC()}, nil
}

func (s *Store) Save(ctx context.Context, record session.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if record.Provider != session.ProviderZerodha || strings.TrimSpace(record.AccessToken) == "" ||
		strings.ContainsAny(record.AccessToken, "\r\n\x00") || record.ExpiresAt.IsZero() ||
		record.AuthenticatedAt.IsZero() || record.AuthenticatedAt.After(record.ExpiresAt) {
		return session.ErrInvalid
	}
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return errors.New("create session store directory")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return errors.New("restrict session store directory")
	}
	temporary, err := os.CreateTemp(directory, ".tradeedge-session-*")
	if err != nil {
		return errors.New("create temporary session store")
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err = temporary.Chmod(0o600); err == nil {
		encoder := json.NewEncoder(temporary)
		encoder.SetEscapeHTML(false)
		err = encoder.Encode(document{
			SchemaVersion: SchemaVersion, Provider: record.Provider, AccessToken: record.AccessToken,
			ExpiresAt: record.ExpiresAt.UTC(), AuthenticatedAt: record.AuthenticatedAt.UTC(),
		})
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("write session store")
	}
	if err = replaceFile(temporaryPath, s.path); err != nil {
		return errors.New("replace session store")
	}
	if err = os.Chmod(s.path, 0o600); err != nil {
		return errors.New("restrict session store")
	}
	if err = syncDirectory(directory); err != nil && runtime.GOOS != "windows" {
		return errors.New("sync session store directory")
	}
	return nil
}

func (s *Store) Delete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := os.Remove(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete session store: %w", err)
	}
	if err = syncDirectory(filepath.Dir(s.path)); err != nil && runtime.GOOS != "windows" {
		return errors.New("sync session store directory")
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
