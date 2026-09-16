//go:build !windows

package sftp

import (
	"bytes"
	"io"
	"os"
	"syscall"
	"testing"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExport_ChownAppliedWhenSetOwner does not exist on Windows: os.Chown is
// unconditionally unsupported there (always returns syscall.EWINDOWS), so
// there is no real uid/gid ownership for this test to verify.
func TestExport_ChownAppliedWhenSetOwner(t *testing.T) {
	ts := newTestServer(t)
	if err := os.MkdirAll(ts.realPath("/repo"), 0750); err != nil {
		t.Fatalf("mkdir /repo: %v", err)
	}

	s := ts.newTestExportSftp(t, "/repo")
	s.setOwner = true
	records := make(chan *connectors.Record, 16)
	results, wait := runExporter(t, s, records)

	// Use the current process uid/gid so Chown succeeds without root.
	uid := uint64(os.Getuid())
	gid := uint64(os.Getgid())
	content := []byte("chown test")
	records <- connectors.NewRecord("/file.txt", "",
		objects.FileInfo{Lmode: 0644, Luid: uid, Lgid: gid},
		nil,
		func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(content)), nil
		},
	)
	close(records)

	got := drainResults(results)
	require.NoError(t, wait())
	require.Len(t, got, 1)
	assert.NoError(t, got[0].Err, "chown with current uid/gid should succeed")

	// Verify the file was written and ownership matches.
	info, err := ts.realStat("/repo/file.txt")
	require.NoError(t, err)
	stat, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok, "expected *syscall.Stat_t from file info")
	assert.Equal(t, uid, uint64(stat.Uid), "file uid must match the record's Luid")
	assert.Equal(t, gid, uint64(stat.Gid), "file gid must match the record's Lgid")
}
