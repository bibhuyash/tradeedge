//go:build !windows

package sessionfile

import "os"

func replaceFile(source, destination string) error { return os.Rename(source, destination) }
