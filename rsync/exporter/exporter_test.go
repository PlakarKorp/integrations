package exporter_test

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integrations/rsync/exporter"
	"github.com/PlakarKorp/integrations/rsync/importer"
	"github.com/PlakarKorp/integrations/rsync/internal/rsynctest"
	"github.com/gokrazy/rsync/rsyncd"
	"github.com/stretchr/testify/require"
)

var mtime = time.Unix(1700000000, 0)

// startServer serves fsys as module "src", and a temporary directory, which
// it returns, as the writable module "dst".
func startServer(t *testing.T, fsys fs.FS) (string, string) {
	dst := t.TempDir()
	return rsynctest.StartServer(t,
		rsyncd.Module{Name: "src", FS: fsys},
		rsyncd.Module{Name: "dst", Path: dst, Writable: true},
	), dst
}

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"a.txt":     {Data: []byte("aaa"), Mode: 0o644, ModTime: mtime},
		"dir":       {Mode: fs.ModeDir | 0o750, ModTime: mtime},
		"dir/b.txt": {Data: []byte("bbb"), Mode: 0o600, ModTime: mtime},
		"dir/ro":    {Mode: fs.ModeDir | 0o555, ModTime: mtime},
		"dir/ro/c":  {Data: []byte("ccc"), Mode: 0o444, ModTime: mtime},
		"dir/link":  {Data: []byte("b.txt"), Mode: fs.ModeSymlink | 0o777, ModTime: mtime},
	}
}

func newExporter(t *testing.T, location string) integration.Exporter {
	t.Helper()
	exp, err := exporter.NewExporter(context.Background(), &integration.Options{}, "rsync", map[string]string{"location": location})
	require.NoError(t, err)
	t.Cleanup(func() { _ = exp.Close(context.Background()) })
	return exp
}

// backup imports location with its content.
func backup(t *testing.T, location string) map[string]rsynctest.Entry {
	t.Helper()
	imp, err := importer.NewImporter(context.Background(), &integration.Options{}, "rsync", map[string]string{"location": location})
	require.NoError(t, err)
	t.Cleanup(func() { _ = imp.Close(context.Background()) })
	return rsynctest.Backup(t, imp)
}

// restore exports pathname of backup with exp, and requires every record
// to succeed.
func restore(t *testing.T, exp integration.Exporter, backup map[string]rsynctest.Entry, pathname string) {
	t.Helper()
	records := rsynctest.Restore(backup, pathname)
	got := rsynctest.Export(t, exp, records...)
	require.Len(t, got, len(records))
	for name, err := range got {
		require.NoError(t, err, name)
	}
}

func TestExport(t *testing.T) {
	addr, dst := startServer(t, testFS())
	saved := backup(t, "rsync://"+addr+"/src")

	// The destination does not exist yet.
	exp := newExporter(t, "rsync://"+addr+"/dst/restore/here")
	require.NoError(t, exp.Ping(context.Background()))
	restore(t, exp, saved, "/src")

	root := filepath.Join(dst, "restore", "here")
	// The restored modes are read-only: let the test clean up.
	t.Cleanup(func() {
		_ = os.Chmod(root, 0o755)
		_ = os.Chmod(filepath.Join(root, "dir", "ro"), 0o755)
	})
	for name, want := range map[string]string{"a.txt": "aaa", "dir/b.txt": "bbb", "dir/ro/c": "ccc"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err, name)
		require.Equal(t, want, string(data), name)
	}
	for name, want := range map[string]fs.FileMode{
		"a.txt":     0o644,
		"dir":       fs.ModeDir | 0o750,
		"dir/b.txt": 0o600,
		"dir/ro":    fs.ModeDir | 0o555,
		"dir/ro/c":  0o444,
	} {
		fi, err := os.Lstat(filepath.Join(root, name))
		require.NoError(t, err, name)
		require.Equal(t, want, fi.Mode(), name)
		require.True(t, fi.ModTime().Equal(mtime), "%s: %v", name, fi.ModTime())
	}
	target, err := os.Readlink(filepath.Join(root, "dir", "link"))
	require.NoError(t, err)
	require.Equal(t, "b.txt", target)
}

func TestExportFile(t *testing.T) {
	addr, dst := startServer(t, testFS())
	saved := backup(t, "rsync://"+addr+"/src")

	exp := newExporter(t, "rsync://"+addr+"/dst")
	restore(t, exp, saved, "/src/dir/b.txt")

	data, err := os.ReadFile(filepath.Join(dst, "b.txt"))
	require.NoError(t, err)
	require.Equal(t, "bbb", string(data))
}

func TestExportUnsupported(t *testing.T) {
	addr, dst := startServer(t, testFS())
	exp := newExporter(t, "rsync://"+addr+"/dst")

	open := func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("")), nil }
	fifo := integration.NewRecord("/fifo", "", integration.FileInfo{Lname: "fifo", Lmode: fs.ModeNamedPipe | 0o644, LmodTime: mtime}, nil, open)
	xattr := integration.NewXattr("/a.txt", "user.test", integration.AttributeExtended, open)

	got := rsynctest.Export(t, exp, fifo, xattr)
	require.ErrorContains(t, got["/fifo"], "unsupported file type")
	require.NoError(t, got["/a.txt"])
	entries, err := os.ReadDir(dst)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestExportReadOnlyModule(t *testing.T) {
	addr, _ := startServer(t, testFS())
	exp := newExporter(t, "rsync://"+addr+"/src")

	open := func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("aaa")), nil }
	got := rsynctest.Export(t, exp, integration.NewRecord("/new.txt", "", integration.FileInfo{Lname: "new.txt", Lsize: 3, Lmode: 0o644, LmodTime: mtime}, nil, open))
	require.Error(t, got["/new.txt"])
}
