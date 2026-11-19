package integration

import (
	"io"
	"io/fs"
	"maps"
	"slices"
	"testing"

	sdk "github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integrations/sftp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func importAll(t *testing.T, s *sshServer, root string, opts *sdk.Options) map[string]*sdk.Record {
	t.Helper()
	imp, err := sftp.NewImporter(t.Context(), opts, "sftp", s.config(root))
	require.NoError(t, err)
	t.Cleanup(func() { _ = imp.Close(t.Context()) })

	records := make(chan *sdk.Record)
	done := make(chan error, 1)
	go func() {
		_, err := imp.Import(t.Context(), records, nil)
		done <- err
	}()

	got := map[string]*sdk.Record{}
	for r := range records {
		require.NoError(t, r.Err, "%s", r.Pathname)
		got[r.Pathname] = r
	}
	require.NoError(t, <-done)
	return got
}

func TestImport(t *testing.T) {
	s := getServer(t)
	dir := testDir(t)
	s.sh(t, `
		set -e
		mkdir -p "$1/sub" "$1/excluded"
		cd "$1"
		printf hello > file.txt && chmod 640 file.txt
		: > empty && chmod 600 empty
		printf spaced > "with space.txt"
		printf nested > sub/nested.txt
		chmod 750 sub
		ln -s file.txt link
		printf skip > excluded/skip.txt
	`, dir)

	opts := options()
	opts.Excludes = []string{dir + "/excluded/**"}
	got := importAll(t, s, dir, opts)

	want := []string{
		dir,
		dir + "/empty",
		dir + "/file.txt",
		dir + "/link",
		dir + "/sub",
		dir + "/sub/nested.txt",
		dir + "/with space.txt",
	}
	require.Equal(t, want, slices.Sorted(maps.Keys(got)))

	uid := s.uid(t)
	file := got[dir+"/file.txt"]
	assert.Equal(t, fs.FileMode(0640), file.FileInfo.Lmode)
	assert.Equal(t, int64(5), file.FileInfo.Lsize)
	assert.Equal(t, uid, file.FileInfo.Luid)

	assert.Equal(t, fs.ModeDir|0750, got[dir+"/sub"].FileInfo.Lmode)
	assert.Equal(t, fs.FileMode(0600), got[dir+"/empty"].FileInfo.Lmode)

	link := got[dir+"/link"]
	assert.Equal(t, fs.ModeSymlink, link.FileInfo.Lmode.Type())
	assert.Equal(t, "file.txt", link.Target)

	for p, content := range map[string]string{
		dir + "/file.txt":       "hello",
		dir + "/empty":          "",
		dir + "/with space.txt": "spaced",
		dir + "/sub/nested.txt": "nested",
	} {
		b, err := io.ReadAll(got[p].Reader)
		require.NoError(t, err, "%s", p)
		assert.Equal(t, content, string(b), "%s", p)
	}
}
