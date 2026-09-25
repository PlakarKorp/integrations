//go:build !windows

package sftp

import (
	"os"
	"syscall"
	"testing"
)

// fileOwner returns the uid/gid a restored file was left with.
func fileOwner(t *testing.T, fi os.FileInfo) (uid, gid uint64) {
	t.Helper()

	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unexpected Sys() type %T", fi.Sys())
	}
	return uint64(st.Uid), uint64(st.Gid)
}
