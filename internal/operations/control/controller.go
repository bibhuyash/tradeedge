// Package control owns the local, auditable STOP_NEW_EXPOSURE and EOD_CLOSE
// boundary. It contains no order, strategy-enable, or threshold mutation API.
package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	"github.com/bibhuyash/tradeedge/internal/tradingruntime"
)

var (
	ErrInvalid  = errors.New("invalid operator control request")
	ErrConflict = errors.New("operator control request identity conflict")
	ErrCorrupt  = errors.New("operator control state corrupt")
)

const SchemaVersion = "tradeedge-operator-controls/v1"

type EODState string

const (
	EODIdle      EODState = "IDLE"
	EODRequested EODState = "REQUESTED"
	EODRunning   EODState = "RUNNING"
	EODCompleted EODState = "COMPLETED"
	EODFailed    EODState = "FAILED"
)

type Command struct {
	Revision  uint64    `json:"revision"`
	RequestID string    `json:"request_id"`
	Action    string    `json:"action"`
	Reason    string    `json:"reason"`
	At        time.Time `json:"at"`
}

type Snapshot struct {
	SchemaVersion      string    `json:"schema_version"`
	Revision           uint64    `json:"revision"`
	NewExposureBlocked bool      `json:"new_exposure_blocked"`
	EOD                EODState  `json:"eod_state"`
	LastError          string    `json:"last_error,omitempty"`
	Commands           []Command `json:"commands"`
	Checksum           string    `json:"checksum"`
}

type EODRunner interface{ RunEOD(context.Context) error }

type Controller struct {
	path     string
	clock    func() time.Time
	runner   EODRunner
	mu       sync.Mutex
	snapshot Snapshot
	requests map[string]string
	ctx      context.Context
	cancel   context.CancelFunc
	wait     sync.WaitGroup
}

func New(path string, runner EODRunner, clock func() time.Time) (*Controller, error) {
	clean, err := filepath.Abs(filepath.Clean(path))
	if err != nil || strings.TrimSpace(path) == "" || filepath.Ext(clean) == "" {
		return nil, ErrInvalid
	}
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	ctx, cancel := context.WithCancel(context.Background())
	value := &Controller{path: clean, runner: runner, clock: clock, requests: map[string]string{}, ctx: ctx, cancel: cancel}
	loaded, loadErr := load(clean)
	if loadErr == nil {
		value.snapshot = loaded
		for _, command := range loaded.Commands {
			value.requests[command.RequestID] = command.Action + "|" + command.Reason
		}
		if loaded.EOD == EODRequested || loaded.EOD == EODRunning {
			value.snapshot.NewExposureBlocked, value.snapshot.EOD, value.snapshot.LastError = true, EODFailed, "UNCLEAN_EOD_RESTART"
			if err := value.persistLocked(); err != nil {
				cancel()
				return nil, err
			}
		}
	} else if errors.Is(loadErr, os.ErrNotExist) {
		value.snapshot = Snapshot{SchemaVersion: SchemaVersion, EOD: EODIdle, Commands: []Command{}}
		if err := value.persistLocked(); err != nil {
			cancel()
			return nil, err
		}
	} else {
		cancel()
		return nil, loadErr
	}
	return value, nil
}

func (c *Controller) StopNewExposure(ctx context.Context, requestID, reason string) (Snapshot, error) {
	return c.command(ctx, requestID, "STOP_NEW_EXPOSURE", reason, false)
}

func (c *Controller) RequestEOD(ctx context.Context, requestID, reason string) (Snapshot, error) {
	snapshot, err := c.command(ctx, requestID, "EOD_CLOSE", reason, true)
	if err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func (c *Controller) command(ctx context.Context, requestID, action, reason string, eod bool) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	requestID, reason = strings.TrimSpace(requestID), strings.ToUpper(strings.TrimSpace(reason))
	if !stable.MatchString(requestID) || !stable.MatchString(reason) {
		return Snapshot{}, ErrInvalid
	}
	c.mu.Lock()
	previous := clone(c.snapshot)
	key := action + "|" + reason
	if existing, found := c.requests[requestID]; found {
		result := clone(c.snapshot)
		c.mu.Unlock()
		if existing != key {
			return result, ErrConflict
		}
		return result, nil
	}
	c.snapshot.Revision++
	c.snapshot.NewExposureBlocked = true
	if eod {
		if c.snapshot.EOD == EODCompleted {
			result := clone(c.snapshot)
			c.mu.Unlock()
			return result, ErrConflict
		}
		c.snapshot.EOD = EODRequested
	}
	command := Command{Revision: c.snapshot.Revision, RequestID: requestID, Action: action, Reason: reason, At: c.clock().UTC()}
	c.snapshot.Commands = append(c.snapshot.Commands, command)
	if len(c.snapshot.Commands) > 1024 {
		c.snapshot.Commands = append([]Command(nil), c.snapshot.Commands[len(c.snapshot.Commands)-1024:]...)
	}
	c.requests[requestID] = key
	if err := c.persistLocked(); err != nil {
		delete(c.requests, requestID)
		c.snapshot = previous
		c.mu.Unlock()
		return Snapshot{}, err
	}
	result := clone(c.snapshot)
	if eod && c.runner != nil {
		c.wait.Add(1)
		go c.runEOD()
	}
	c.mu.Unlock()
	return result, nil
}

func (c *Controller) runEOD() {
	defer c.wait.Done()
	c.mu.Lock()
	if c.snapshot.EOD != EODRequested {
		c.mu.Unlock()
		return
	}
	c.snapshot.EOD, c.snapshot.Revision = EODRunning, c.snapshot.Revision+1
	if err := c.persistLocked(); err != nil {
		c.snapshot.EOD, c.snapshot.LastError = EODFailed, "CONTROL_PERSISTENCE_FAILED"
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	err := c.runner.RunEOD(c.ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshot.Revision++
	if err != nil {
		c.snapshot.EOD, c.snapshot.LastError = EODFailed, "EOD_SEQUENCE_FAILED"
	} else {
		c.snapshot.EOD, c.snapshot.LastError = EODCompleted, ""
	}
	_ = c.persistLocked()
}

func (c *Controller) Snapshot() Snapshot { c.mu.Lock(); defer c.mu.Unlock(); return clone(c.snapshot) }

func (c *Controller) Controls(context.Context) (tradingruntime.ControlSnapshot, error) {
	value := c.Snapshot()
	return tradingruntime.ControlSnapshot{NewExposureBlocked: value.NewExposureBlocked, EvidenceRevision: value.Checksum, Portfolios: map[portfoliomodel.PortfolioID]bool{}, Strategies: map[domain.StrategyID]bool{}, CircuitOpen: map[string]bool{}}, nil
}

func (c *Controller) Shutdown(ctx context.Context) error {
	c.cancel()
	done := make(chan struct{})
	go func() { c.wait.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Controller) persistLocked() error {
	validated, err := finalize(c.snapshot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o750); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(validated, "", "  ")
	raw = append(raw, '\n')
	temporary := c.path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(raw)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(temporary, c.path); err != nil {
		return err
	}
	c.snapshot = validated
	return nil
}

func finalize(value Snapshot) (Snapshot, error) {
	value.Checksum = ""
	if value.SchemaVersion != SchemaVersion || len(value.Commands) > 1024 {
		return Snapshot{}, ErrCorrupt
	}
	switch value.EOD {
	case EODIdle, EODRequested, EODRunning, EODCompleted, EODFailed:
	default:
		return Snapshot{}, ErrCorrupt
	}
	for index, command := range value.Commands {
		if command.Revision == 0 || command.RequestID == "" || command.Reason == "" || command.At.IsZero() || (command.Action != "STOP_NEW_EXPOSURE" && command.Action != "EOD_CLOSE") || (index > 0 && value.Commands[index-1].Revision >= command.Revision) {
			return Snapshot{}, ErrCorrupt
		}
	}
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	value.Checksum = hex.EncodeToString(sum[:])
	return value, nil
}

func load(path string) (Snapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value Snapshot
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Snapshot{}, ErrCorrupt
	}
	expected := value.Checksum
	validated, err := finalize(value)
	if err != nil || validated.Checksum != expected {
		return Snapshot{}, ErrCorrupt
	}
	return value, nil
}

func clone(value Snapshot) Snapshot {
	value.Commands = append([]Command(nil), value.Commands...)
	return value
}

var stable = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_.:-]{0,127}$`)

type Handler struct{ Controller *Controller }

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Controller == nil {
		write(w, 503, map[string]string{"error": "control_unavailable"})
		return
	}
	path := r.URL.Path
	if strings.HasPrefix(path, "/api/v1/operations") {
		path = strings.TrimPrefix(path, "/api/v1/operations")
	}
	if r.Method == http.MethodGet && path == "/v1/status" {
		write(w, 200, h.Controller.Snapshot())
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		write(w, 405, map[string]string{"error": "method_not_allowed"})
		return
	}
	var request struct {
		RequestID string `json:"request_id"`
		Reason    string `json:"reason"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil {
		write(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	var value Snapshot
	var err error
	switch path {
	case "/v1/stop-new-exposure":
		value, err = h.Controller.StopNewExposure(r.Context(), request.RequestID, request.Reason)
	case "/v1/eod-close":
		value, err = h.Controller.RequestEOD(r.Context(), request.RequestID, request.Reason)
	default:
		write(w, 404, map[string]string{"error": "not_found"})
		return
	}
	if errors.Is(err, ErrConflict) {
		write(w, 409, map[string]string{"error": "request_conflict"})
		return
	}
	if err != nil {
		write(w, 503, map[string]string{"error": "control_failed"})
		return
	}
	write(w, 202, value)
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type LocalServer struct {
	server   *http.Server
	listener net.Listener
	socket   string
}

func StartLocalServer(socket string, handler http.Handler) (*LocalServer, error) {
	if handler == nil || strings.TrimSpace(socket) == "" {
		return nil, ErrInvalid
	}
	clean := filepath.Clean(socket)
	if err := os.MkdirAll(filepath.Dir(clean), 0o750); err != nil {
		return nil, err
	}
	if _, err := os.Stat(clean); err == nil {
		return nil, ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", clean)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(clean, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(clean)
		return nil, err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	value := &LocalServer{server: server, listener: listener, socket: clean}
	go func() { _ = server.Serve(listener) }()
	return value, nil
}
func (s *LocalServer) Shutdown(ctx context.Context) error {
	err := s.server.Shutdown(ctx)
	removeErr := os.Remove(s.socket)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	return errors.Join(err, removeErr)
}
