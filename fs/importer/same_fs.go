//go:build !windows

package importer

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

func open(p string) (*os.File, error) {
	fp, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0644)
	if err != nil {
		return nil, err
	}

	fi, err := fp.Stat()
	if err != nil {
		fp.Close()
		return nil, err
	}

	if !fi.Mode().IsRegular() {
		fp.Close()
		return nil, fmt.Errorf("not a regular file (%s)", fi.Mode())
	}

	return fp, nil
}

func dirDevice(info os.FileInfo) uint64 {
	if sb, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(sb.Dev)
	}
	return 0
}

func isSameFs(devno uint64, info fs.FileInfo) bool {
	if sb, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(sb.Dev) == devno
	}

	return true
}
