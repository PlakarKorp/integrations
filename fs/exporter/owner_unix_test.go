//go:build !windows

package exporter

import (
	"os"
	"syscall"
	"testing"
)

// fileOwner returns the uid/gid a restored file was left with.
func fileOwner(t *testing.T, fi os.FileInfo) (uid, gid uint32) {
	t.Helper()

	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unexpected Sys() type %T", fi.Sys())
	}
	return uint32(st.Uid), uint32(st.Gid)
}
