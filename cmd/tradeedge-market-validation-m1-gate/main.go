package main

import (
	"encoding/json"
	"os"

	"github.com/bibhuyash/tradeedge/internal/releasegate/marketvalidationm1"
)

func main() {
	report := marketvalidationm1.Run()
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if encoder.Encode(report) != nil || !report.Passed {
		os.Exit(1)
	}
}
