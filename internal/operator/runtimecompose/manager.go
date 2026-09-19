package runtimecompose

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
)

type Commander interface {
	Run(context.Context, string, ...string) ([]byte, error)
}
type execCommander struct{ directory string }

func (c execCommander) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = c.directory
	return cmd.CombinedOutput()
}

type Manager struct {
	directory string
	command   Commander
}

func New(directory string) (*Manager, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("compose repository is required")
	}
	return &Manager{directory: directory, command: execCommander{directory: directory}}, nil
}
func NewWithCommander(directory string, command Commander) (*Manager, error) {
	if strings.TrimSpace(directory) == "" || command == nil {
		return nil, errors.New("compose configuration is required")
	}
	return &Manager{directory: directory, command: command}, nil
}

type psRecord struct {
	State  string `json:"State"`
	Health string `json:"Health"`
}

func (m *Manager) Status(ctx context.Context) (bool, bool, error) {
	raw, err := m.command.Run(ctx, "docker", "compose", "--project-name", "tradeedge", "--env-file", ".env", "ps", "--format", "json", "tradeedge-shadow")
	if err != nil {
		return false, false, errors.New("query SHADOW runtime")
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return false, false, nil
	}
	var records []psRecord
	if strings.HasPrefix(trimmed, "[") {
		if json.Unmarshal(raw, &records) != nil {
			return false, false, errors.New("decode SHADOW runtime status")
		}
	} else {
		for _, line := range strings.Split(trimmed, "\n") {
			var record psRecord
			if json.Unmarshal([]byte(line), &record) != nil {
				return false, false, errors.New("decode SHADOW runtime status")
			}
			records = append(records, record)
		}
	}
	if len(records) != 1 {
		return false, false, errors.New("ambiguous SHADOW runtime status")
	}
	running := strings.EqualFold(records[0].State, "running")
	healthy := running && strings.EqualFold(records[0].Health, "healthy")
	return running, healthy, nil
}
func (m *Manager) StartShadow(ctx context.Context) error {
	running, healthy, err := m.Status(ctx)
	if err != nil {
		return err
	}
	if running && healthy {
		return nil
	}
	if _, err = m.command.Run(ctx, "docker", "compose", "--project-name", "tradeedge", "--env-file", ".env", "up", "-d", "tradeedge-shadow"); err != nil {
		return errors.New("start SHADOW runtime")
	}
	return nil
}
