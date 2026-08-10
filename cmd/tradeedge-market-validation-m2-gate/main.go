package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"

	"github.com/bibhuyash/tradeedge/internal/releasegate/marketvalidationm2"
)

func main() {
	report := marketvalidationm2.Run()
	output := flag.String("output", "", "create-once M2 evidence JSON")
	flag.Parse()
	var destination io.Writer = os.Stdout
	var file *os.File
	if *output != "" {
		if err := os.MkdirAll(filepath.Dir(filepath.Clean(*output)), 0o750); err != nil {
			os.Exit(1)
		}
		var err error
		file, err = os.OpenFile(filepath.Clean(*output), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
		if err != nil {
			os.Exit(1)
		}
		defer file.Close()
		destination = file
	}
	encoder := json.NewEncoder(destination)
	encoder.SetIndent("", "  ")
	if encoder.Encode(report) != nil || !report.Passed {
		os.Exit(1)
	}
}
