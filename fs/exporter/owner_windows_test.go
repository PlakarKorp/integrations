//go:build windows

package exporter

import (
	"os"
	"testing"
)

// fileOwner is unreachable on Windows: TestExport_SkipOwnership requires
// os.Geteuid() == 0, which os.Geteuid() never returns on this platform.
func fileOwner(t *testing.T, fi os.FileInfo) (uid, gid uint32) {
	t.Helper()
	t.Fatal("fileOwner is not supported on windows")
	return 0, 0
}
