package main

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/bibhuyash/tradeedge/internal/releasegate/phase7closure"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r := phase7closure.Run(ctx)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if enc.Encode(r) != nil || !r.Passed {
		os.Exit(1)
	}
}
