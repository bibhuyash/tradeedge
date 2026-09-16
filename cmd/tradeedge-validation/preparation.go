package main

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"time"

	"github.com/bibhuyash/tradeedge/internal/marketvalidation"
)

func prepareSession(args []string) error {
	set := flag.NewFlagSet("prepare-session", flag.ContinueOnError)
	authorization := set.String("authorization", "", "existing SHADOW authorization manifest (optional until prepared)")
	date := set.String("date", "", "session date YYYY-MM-DD")
	commit := set.String("commit", "", "full intended application commit")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return errors.New("prepare-session does not accept positional arguments")
	}
	report, err := marketvalidation.InspectSessionPreparation(*authorization, *date, *commit, time.Now())
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return err
	}
	if report.Status == "BLOCKED" {
		return errors.New("session preparation blocked; inspect the diagnostic checks")
	}
	return nil
}
