package main

import "testing"

func TestPreparationCommandRejectsMissingOrAmbiguousInputs(t *testing.T) {
	for _, args := range [][]string{{"prepare-session"}, {"prepare-session", "-date", "not-a-date", "-commit", "HEAD"}, {"prepare-session", "unexpected"}} {
		if err := run(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
