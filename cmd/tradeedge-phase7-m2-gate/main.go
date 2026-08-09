package main

import (
	"encoding/json"
	"github.com/bibhuyash/tradeedge/internal/operations/releasegate"
	"os"
)

func main() {
	report := releasegate.Run()
	_ = json.NewEncoder(os.Stdout).Encode(report)
	if !report.Passed {
		os.Exit(1)
	}
}
