package main

import (
	"os"

	"github.com/bibhuyash/tradeedge/internal/operator/preparation"
)

func main() { os.Exit(preparation.ExecuteCLI(os.Args[1:], os.Stdout, os.Stderr)) }
