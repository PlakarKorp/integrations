//go:build windows

package sftp

import (
	"os"
	"testing"
)

// fileOwner is unreachable on Windows: TestExport_ChownAppliedWhenSetOwner
// skips itself there before ever calling this.
func fileOwner(t *testing.T, fi os.FileInfo) (uid, gid uint64) {
	t.Helper()
	t.Fatal("fileOwner is not supported on windows")
	return 0, 0
}
