package main

import (
	"io"
	"time"

	"github.com/bibhuyash/tradeedge/internal/operator/preparation"
)

type runner interface {
	Run(string, ...string) (string, error)
}

type options struct {
	repository, credentialsFile, selectorsFile, sessionFile, validationCommand string
	now                                                                        time.Time
	readOnly                                                                   func([]string, string) (string, error)
	acceptanceOnly                                                             bool
}

func prepare(v options, r runner, output io.Writer) error {
	return preparation.Prepare(preparation.Config{Repository: v.repository, CredentialsFile: v.credentialsFile, SelectorsFile: v.selectorsFile, SessionFile: v.sessionFile, ValidationCommand: v.validationCommand, Now: v.now, ReadOnly: v.readOnly, AcceptanceOnly: v.acceptanceOnly}, r, output)
}

func preflightDiagnostic(raw string) string { return preparation.PreflightDiagnostic(raw) }
func writePreparationFailure(output io.Writer, err error) {
	preparation.WritePreparationFailure(output, err)
}
