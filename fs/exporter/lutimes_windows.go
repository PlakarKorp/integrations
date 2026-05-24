//go:build windows

package exporter

import (
	"time"

	"golang.org/x/sys/windows"
)

func Lutimes(path string, atime time.Time, mtime time.Time) error {
	handle, err := windows.Open(path, windows.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer windows.Close(handle)

	var (
		access   = windows.NsecToFiletime(atime.UnixNano())
		creation = windows.NsecToFiletime(mtime.UnixNano())
	)

	return windows.SetFileTime(handle, &creation, &access, &creation)
}
