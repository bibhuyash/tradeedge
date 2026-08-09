package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/bibhuyash/tradeedge/internal/tradingruntime/releasegate"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := releasegate.Run(ctx)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if encodeErr := encoder.Encode(report); encodeErr != nil {
		fmt.Fprintln(os.Stderr, encodeErr)
		os.Exit(1)
	}
	if err != nil || !report.Passed {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
