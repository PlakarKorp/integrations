package integration

import (
	"io"
	"io/fs"
	"strings"
	"testing"

	sdk "github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integrations/sftp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dirRecord(p string, mode fs.FileMode, uid uint64) *sdk.Record {
	return sdk.NewRecord(p, "", sdk.FileInfo{Lmode: fs.ModeDir | mode, Luid: uid, Lgid: uid}, nil, nil)
}

func fileRecord(p, content string, fi sdk.FileInfo) *sdk.Record {
	fi.Lsize = int64(len(content))
	if fi.Lnlink == 0 {
		fi.Lnlink = 1
	}
	return sdk.NewRecord(p, "", fi, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(content)), nil
	})
}

// exportAll restores records, in order, under root with the given exporter
// configuration on top of the connection parameters, and returns the
// per-record errors by pathname.
func exportAll(t *testing.T, s *sshServer, root string, extra map[string]string, records ...*sdk.Record) map[string]error {
	t.Helper()
	cfg := s.config(root)
	for k, v := range extra {
		cfg[k] = v
	}
	exp, err := sftp.NewExporter(t.Context(), options(), "sftp", cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = exp.Close(t.Context()) })

	in := make(chan *sdk.Record, len(records))
	for _, r := range records {
		in <- r
	}
	close(in)

	results := make(chan *sdk.Result, len(records))
	require.NoError(t, exp.Export(t.Context(), nil, in, results))

	got := map[string]error{}
	for r := range results {
		got[r.Record.Pathname] = r.Err
	}
	require.Len(t, got, len(records))
	return got
}

func TestExport(t *testing.T) {
	s := getServer(t)
	dir := testDir(t)
	uid := s.uid(t)

	hard := sdk.FileInfo{Lmode: 0644, Luid: uid, Lgid: uid, Lnlink: 2, Ldev: 1, Lino: 42}
	got := exportAll(t, s, dir, nil,
		dirRecord("/", 0755, uid),
		dirRecord("/private", 0700, uid),
		dirRecord("/readonly", 0500, uid),
		fileRecord("/private/file.txt", "hello", sdk.FileInfo{Lmode: 0640, Luid: uid, Lgid: uid}),
		fileRecord("/empty", "", sdk.FileInfo{Lmode: 0600, Luid: uid, Lgid: uid}),
		fileRecord("/with space.txt", "spaced", sdk.FileInfo{Lmode: 0644, Luid: uid, Lgid: uid}),
		fileRecord("/hard1", "shared", hard),
		fileRecord("/hard2", "shared", hard),
		sdk.NewRecord("/link", "private/file.txt", sdk.FileInfo{Lmode: fs.ModeSymlink | 0777}, nil, nil),
		fileRecord("/readonly/inside", "ro", sdk.FileInfo{Lmode: 0444, Luid: uid, Lgid: uid}),
	)
	for p, err := range got {
		require.NoError(t, err, "%s", p)
	}

	for p, want := range map[string]struct{ mode, content string }{
		"/private/file.txt": {"640", "hello"},
		"/empty":            {"600", ""},
		"/with space.txt":   {"644", "spaced"},
		"/hard1":            {"644", "shared"},
		"/readonly/inside":  {"444", "ro"},
	} {
		assert.Equal(t, want.mode, s.stat(t, dir+p).mode, "%s", p)
		assert.Equal(t, want.content, s.sh(t, `cat "$1"`, dir+p), "%s", p)
	}
	assert.Equal(t, "700", s.stat(t, dir+"/private").mode)
	assert.Equal(t, "500", s.stat(t, dir+"/readonly").mode)
	assert.Equal(t, s.stat(t, dir+"/hard1").ino, s.stat(t, dir+"/hard2").ino, "hard links share an inode")
	assert.Equal(t, "private/file.txt\n", s.sh(t, `readlink "$1"`, dir+"/link"))
}

func TestExportWithoutSetOwnerKeepsServerUser(t *testing.T) {
	s := getServer(t)
	dir := testDir(t)

	got := exportAll(t, s, dir, nil,
		dirRecord("/", 0755, 0),
		fileRecord("/file", "x", sdk.FileInfo{Lmode: 0644}),
	)
	require.NoError(t, got["/file"])
	assert.Equal(t, strings.TrimSpace(s.sh(t, "id -u")), s.stat(t, dir+"/file").uid)
}

func TestExportSetOwnerToServerUser(t *testing.T) {
	s := getServer(t)
	dir := testDir(t)
	uid := s.uid(t)

	got := exportAll(t, s, dir, map[string]string{"set_owner": "true"},
		dirRecord("/", 0755, uid),
		fileRecord("/file", "x", sdk.FileInfo{Lmode: 0644, Luid: uid, Lgid: uid}),
	)
	require.NoError(t, got["/"])
	require.NoError(t, got["/file"])
}

// The server user is not root, so chown to anyone else is refused.  set_owner
// is opt-in, so that is reported, but the mode is still applied, without
// setuid/setgid.
func TestExportSetOwnerRefused(t *testing.T) {
	s := getServer(t)
	dir := testDir(t)

	got := exportAll(t, s, dir, map[string]string{"set_owner": "true"},
		dirRecord("/", fs.ModeSetgid|0700, 0),
		fileRecord("/tool", "not mine", sdk.FileInfo{Lmode: fs.ModeSetuid | 0750}),
	)
	require.ErrorIs(t, got["/"], fs.ErrPermission)
	require.ErrorIs(t, got["/tool"], fs.ErrPermission)

	assert.Equal(t, "700", s.stat(t, dir).mode)
	assert.Equal(t, "750", s.stat(t, dir+"/tool").mode)
	assert.Equal(t, "not mine", s.sh(t, `cat "$1"`, dir+"/tool"))
}
