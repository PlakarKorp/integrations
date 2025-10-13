//go:build windows

package exporter

import (
	"time"
)

func Lutimes(path string, atime time.Time, mtime time.Time) error {
	// Windows doesn't support Lutimes for symlinks
	return nil
}
