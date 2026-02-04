//go:build !windows

package importer

import (
	"io/fs"
	"os"
	"syscall"
)

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
