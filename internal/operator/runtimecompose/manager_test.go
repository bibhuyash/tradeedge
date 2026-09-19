package runtimecompose

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeCommand struct {
	outputs [][]byte
	errors  []error
	calls   [][]string
}

func (f *fakeCommand) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	i := len(f.calls) - 1
	var out []byte
	var err error
	if i < len(f.outputs) {
		out = f.outputs[i]
	}
	if i < len(f.errors) {
		err = f.errors[i]
	}
	return out, err
}
func TestHealthyRuntimeIsReused(t *testing.T) {
	f := &fakeCommand{outputs: [][]byte{[]byte(`{"State":"running","Health":"healthy"}`)}}
	m, _ := NewWithCommander("repo", f)
	if err := m.StartShadow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls=%d", len(f.calls))
	}
}
func TestStartUsesOnlyFixedArguments(t *testing.T) {
	f := &fakeCommand{outputs: [][]byte{nil, nil}}
	m, _ := NewWithCommander("repo", f)
	if err := m.StartShadow(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"docker", "compose", "--project-name", "tradeedge", "--env-file", ".env", "up", "-d", "tradeedge-shadow"}
	if !reflect.DeepEqual(f.calls[1], want) {
		t.Fatalf("call=%v", f.calls[1])
	}
}
func TestStartFailureIsSafe(t *testing.T) {
	f := &fakeCommand{outputs: [][]byte{nil, nil}, errors: []error{nil, errors.New("boom")}}
	m, _ := NewWithCommander("repo", f)
	if err := m.StartShadow(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}
