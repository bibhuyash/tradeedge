// Command tradeedge-zerodha-auth is the legacy operator wrapper around the
// shared read-only Zerodha integration operations.
package main

import (
	"os"

	zerodhaops "github.com/bibhuyash/tradeedge/internal/integration/zerodha"
)

func main() {
	os.Exit(zerodhaops.ExecuteCLI(os.Args[1:], os.Stdout, os.Stderr))
}
