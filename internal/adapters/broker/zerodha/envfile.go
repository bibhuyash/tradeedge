package zerodha

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	accessTokenKey  = "TRADEEDGE_ZERODHA_ACCESS_TOKEN"
	accessExpiryKey = "TRADEEDGE_ZERODHA_ACCESS_TOKEN_EXPIRES_AT"
)

// LookupWithPersistedSession overlays the supported Zerodha credential fields
// from path. It rejects malformed or partial restored sessions before exposing
// any credential value to the caller.
func LookupWithPersistedSession(base LookupEnv, path string) (LookupEnv, error) {
	if base == nil {
		return nil, ErrCredentialsMissing
	}
	raw, err := os.ReadFile(filepath.Clean(strings.TrimSpace(path)))
	if err != nil {
		return nil, fmt.Errorf("load Zerodha session: %w", err)
	}
	values := map[string]string{}
	credentialKeys := map[string]struct{}{
		"TRADEEDGE_ZERODHA_API_KEY": {}, "TRADEEDGE_ZERODHA_API_SECRET": {}, "TRADEEDGE_ZERODHA_REQUEST_TOKEN": {},
		accessTokenKey: {}, accessExpiryKey: {},
	}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		line := scanner.Text()
		name, _ := dotenvAssignment(line)
		if _, supported := credentialKeys[name]; !supported {
			continue
		}
		if _, duplicate := values[name]; duplicate {
			return nil, ErrCredentialsMalformed
		}
		index := strings.IndexByte(line, '=')
		if index < 0 {
			return nil, ErrCredentialsMalformed
		}
		value := strings.TrimSpace(line[index+1:])
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			if value[0] == '"' {
				decoded, decodeErr := strconv.Unquote(value)
				if decodeErr != nil {
					return nil, ErrCredentialsMalformed
				}
				value = decoded
			} else {
				value = value[1 : len(value)-1]
			}
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, ErrCredentialsMalformed
		}
		values[name] = value
	}
	if err = scanner.Err(); err != nil {
		return nil, errors.Join(ErrCredentialsMalformed, err)
	}
	access, accessPresent := values[accessTokenKey]
	expiry, expiryPresent := values[accessExpiryKey]
	if accessPresent != expiryPresent || (access == "") != (expiry == "") {
		return nil, ErrCredentialsMalformed
	}
	if access == "" {
		delete(values, accessTokenKey)
		delete(values, accessExpiryKey)
	}
	return func(key string) (string, bool) {
		if value, ok := values[key]; ok {
			return value, true
		}
		return base(key)
	}, nil
}

// PersistAccessToken atomically updates only the restored-session fields in an
// existing untracked dotenv file. Secret values are never included in errors.
func (manager *SessionManager) PersistAccessToken(path string) error {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.state != SessionAuthenticated || manager.accessToken == "" || !manager.expiresAt.After(manager.clock.Now()) {
		return ErrAuthentication
	}
	return persistAccessToken(path, manager.accessToken, manager.expiresAt)
}

func persistAccessToken(path, token string, expiresAt time.Time) error {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "." || token == "" || strings.ContainsAny(token, "\r\n\x00") || expiresAt.IsZero() {
		return ErrCredentialsMalformed
	}
	raw, err := os.ReadFile(clean)
	if err != nil {
		return fmt.Errorf("persist Zerodha session: %w", err)
	}
	updated, err := updateDotenvSession(raw, token, expiresAt.UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	info, err := os.Stat(clean)
	if err != nil {
		return fmt.Errorf("persist Zerodha session: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(clean), ".tradeedge-zerodha-session-*")
	if err != nil {
		return fmt.Errorf("persist Zerodha session: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	mode := info.Mode().Perm()
	if mode&0o077 != 0 || mode == 0 {
		mode = 0o600
	}
	if err = temporary.Chmod(mode); err == nil {
		_, err = temporary.Write(updated)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("persist Zerodha session: %w", err)
	}
	if err = replaceFile(temporaryPath, clean); err != nil {
		return fmt.Errorf("persist Zerodha session: %w", err)
	}
	if err = os.Chmod(clean, mode); err != nil {
		return fmt.Errorf("persist Zerodha session: %w", err)
	}
	return nil
}

// PersistPreparationPaths atomically updates only the two non-secret Compose
// selectors owned by the preparation workflow.
func PersistPreparationPaths(path, authorizationManifest, runtimeBundle string) error {
	if strings.TrimSpace(authorizationManifest) == "" || strings.TrimSpace(runtimeBundle) == "" || strings.ContainsAny(authorizationManifest+runtimeBundle, "\r\n\x00") {
		return ErrCredentialsMalformed
	}
	return persistDotenvValues(path, map[string]string{
		"TRADEEDGE_AUTHORIZATION_MANIFEST_HOST": authorizationManifest,
		"TRADEEDGE_RUNTIME_BUNDLE_HOST":         runtimeBundle,
	})
}

// InvalidateRequestToken atomically clears a provider-rejected one-time token
// so repeated preparation cannot submit it again.
func InvalidateRequestToken(path string) error {
	return persistDotenvValues(path, map[string]string{"TRADEEDGE_ZERODHA_REQUEST_TOKEN": ""})
}

func persistDotenvValues(path string, updates map[string]string) error {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "." {
		return ErrCredentialsMalformed
	}
	raw, err := os.ReadFile(clean)
	if err != nil {
		return fmt.Errorf("persist operator configuration: %w", err)
	}
	updated, err := updateDotenvValues(raw, updates)
	if err != nil {
		return err
	}
	info, err := os.Stat(clean)
	if err != nil {
		return fmt.Errorf("persist operator configuration: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(clean), ".tradeedge-operator-config-*")
	if err != nil {
		return fmt.Errorf("persist operator configuration: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	mode := info.Mode().Perm()
	if mode&0o077 != 0 || mode == 0 {
		mode = 0o600
	}
	if err = temporary.Chmod(mode); err == nil {
		_, err = temporary.Write(updated)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("persist operator configuration: %w", err)
	}
	if err = replaceFile(temporaryPath, clean); err != nil {
		return fmt.Errorf("persist operator configuration: %w", err)
	}
	return os.Chmod(clean, mode)
}

func updateDotenvValues(raw []byte, updates map[string]string) ([]byte, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	lines, seen := make([]string, 0, 32), map[string]bool{}
	for scanner.Scan() {
		line := scanner.Text()
		name, assignment := dotenvAssignment(line)
		if value, ok := updates[name]; ok {
			if seen[name] {
				return nil, ErrCredentialsMalformed
			}
			seen[name], line = true, assignment+value
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.Join(ErrCredentialsMalformed, err)
	}
	for name, value := range updates {
		if !seen[name] {
			lines = append(lines, name+"="+value)
		}
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

func updateDotenvSession(raw []byte, token, expiry string) ([]byte, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	lines := make([]string, 0, 32)
	seenToken, seenExpiry := false, false
	for scanner.Scan() {
		line := scanner.Text()
		name, assignment := dotenvAssignment(line)
		switch name {
		case accessTokenKey:
			if seenToken {
				return nil, ErrCredentialsMalformed
			}
			line, seenToken = assignment+token, true
		case accessExpiryKey:
			if seenExpiry {
				return nil, ErrCredentialsMalformed
			}
			line, seenExpiry = assignment+expiry, true
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.Join(ErrCredentialsMalformed, err)
	}
	if !seenToken {
		lines = append(lines, accessTokenKey+"="+token)
	}
	if !seenExpiry {
		lines = append(lines, accessExpiryKey+"="+expiry)
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

func dotenvAssignment(line string) (string, string) {
	trimmed := strings.TrimSpace(line)
	prefix := ""
	if strings.HasPrefix(trimmed, "export ") {
		prefix, trimmed = "export ", strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
	}
	index := strings.IndexByte(trimmed, '=')
	if index <= 0 {
		return "", ""
	}
	name := strings.TrimSpace(trimmed[:index])
	if name != accessTokenKey && name != accessExpiryKey {
		return name, prefix + name + "="
	}
	return name, prefix + name + "="
}
