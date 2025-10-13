//go:build !windows

package exporter

import (
	"time"

	"golang.org/x/sys/unix"
)

// Lutimes sets the access and modification times of the named file.
// If the file is a symlink, it changes the times of the symlink, not the target.
func Lutimes(path string, atime time.Time, mtime time.Time) error {
	var utimes [2]unix.Timeval
	utimes[0] = unix.NsecToTimeval(atime.UnixNano())
	utimes[1] = unix.NsecToTimeval(mtime.UnixNano())
	return unix.Lutimes(path, utimes[0:])
}
