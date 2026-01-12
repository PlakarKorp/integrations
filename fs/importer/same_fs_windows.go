package importer

import (
	"io/fs"
	"os"
)

func dirDevice(info os.FileInfo) uint64 {
	return 0
}

func isSameFs(devno uint64, info fs.FileInfo) bool {
	return true
}
