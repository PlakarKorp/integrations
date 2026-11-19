//go:build !windows

package sftp

import (
	"bytes"
	"io"
	"os"
	"syscall"
	"testing"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// set_owner is opt-in, so a chown the server refuses is reported, as rsync
// --super or tar --same-owner would.  The rest of the restore must still match
// the snapshot: content written, mode applied, but no setuid/setgid on a file
// left owned by the server user.
func TestExport_SetOwnerRefused(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root may chown to any uid")
	}
	uid := uint64(os.Getuid()) + 1
	gid := uint64(os.Getgid()) + 1

	ts := newTestServer(t)
	require.NoError(t, os.MkdirAll(ts.realPath("/repo"), 0750))

	s := ts.newTestExportSftp(t, "/repo")
	s.setOwner = true
	records := make(chan *integration.Record, 16)
	results, wait := runExporter(t, s, records)

	content := []byte("not mine")
	records <- integration.NewRecord("/private", "",
		integration.FileInfo{Lmode: os.ModeDir | os.ModeSetgid | 0700, Luid: uid, Lgid: gid},
		nil, nil)
	records <- integration.NewRecord("/private/tool", "",
		integration.FileInfo{Lmode: os.ModeSetuid | 0750, Luid: uid, Lgid: gid, Lnlink: 1},
		nil,
		func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(content)), nil
		},
	)
	close(records)

	got := drainResults(results)
	require.NoError(t, wait())
	require.Len(t, got, 2)
	for _, r := range got {
		require.ErrorIs(t, r.Err, os.ErrPermission, "%s", r.Record.Pathname)
	}

	dir, err := ts.client.Stat("/repo/private")
	require.NoError(t, err)
	assert.Equal(t, os.ModeDir|0700, dir.Mode())

	file, err := ts.client.Stat("/repo/private/tool")
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0750), file.Mode())
	onDisk, err := os.ReadFile(ts.realPath("/repo/private/tool"))
	require.NoError(t, err)
	assert.Equal(t, content, onDisk)
}
