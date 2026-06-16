//go:build windows

package exporter

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

func Lutimes(path string, atime time.Time, mtime time.Time) error {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}

	handle, err := windows.CreateFile(
		pathPtr,
		windows.FILE_WRITE_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return fmt.Errorf("CreateFile: %w", err)
	}
	defer windows.Close(handle)

	var (
		access   = windows.NsecToFiletime(atime.UnixNano())
		creation = windows.NsecToFiletime(mtime.UnixNano())
	)

	return windows.SetFileTime(handle, &creation, &access, &creation)
}
