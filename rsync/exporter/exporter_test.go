package exporter_test

import (
	"context"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/PlakarKorp/integrations/rsync/exporter"
	"github.com/PlakarKorp/integrations/rsync/importer"
	"github.com/PlakarKorp/kloset/connectors"
	kexporter "github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/repository"
	"github.com/PlakarKorp/kloset/snapshot"
	ktest "github.com/PlakarKorp/kloset/testing"
	"github.com/gokrazy/rsync/rsyncd"
	"github.com/stretchr/testify/require"
)

var mtime = time.Unix(1700000000, 0)

// startServer serves fsys as module "src", and a temporary directory, which
// it returns, as the writable module "dst".
func startServer(t *testing.T, fsys fs.FS) (string, string) {
	t.Helper()
	dst := t.TempDir()
	srv, err := rsyncd.NewServer([]rsyncd.Module{
		{Name: "src", FS: fsys},
		{Name: "dst", Path: dst, Writable: true},
	}, rsyncd.DontRestrict())
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
	})
	return ln.Addr().String(), dst
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

func newExporter(t *testing.T, location string) kexporter.Exporter {
	t.Helper()
	exp, err := exporter.NewExporter(context.Background(), &connectors.Options{}, "rsync", map[string]string{"location": location})
	require.NoError(t, err)
	t.Cleanup(func() { _ = exp.Close(context.Background()) })
	return exp
}

// backup backs up location into a new repository and returns the snapshot.
func backup(t *testing.T, location string) *snapshot.Snapshot {
	t.Helper()
	repo := ktest.GenerateRepository(t, nil, nil, nil)
	imp, err := importer.NewImporter(context.Background(), &connectors.Options{}, "rsync", map[string]string{"location": location})
	require.NoError(t, err)
	builder, err := snapshot.Create(repo, repository.DefaultType, "", objects.NilMac, &snapshot.BuilderOptions{Name: "test"})
	require.NoError(t, err)
	src, err := snapshot.NewSource(repo.AppContext(), imp)
	require.NoError(t, err)
	require.NoError(t, builder.Backup(src))
	require.NoError(t, builder.Commit())
	require.NoError(t, builder.Close())
	require.NoError(t, repo.RebuildState())

	snap, err := snapshot.Load(repo, builder.Header.Identifier)
	require.NoError(t, err)
	t.Cleanup(func() { _ = snap.Close() })
	return snap
}

func TestExport(t *testing.T) {
	addr, dst := startServer(t, testFS())
	snap := backup(t, "rsync://"+addr+"/src")

	// The destination does not exist yet.
	exp := newExporter(t, "rsync://"+addr+"/dst/restore/here")
	require.NoError(t, exp.Ping(context.Background()))
	require.NoError(t, snap.Export(exp, "/src", &snapshot.ExportOptions{}))

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
	snap := backup(t, "rsync://"+addr+"/src")

	exp := newExporter(t, "rsync://"+addr+"/dst")
	require.NoError(t, snap.Export(exp, "/src/dir/b.txt", &snapshot.ExportOptions{}))

	data, err := os.ReadFile(filepath.Join(dst, "b.txt"))
	require.NoError(t, err)
	require.Equal(t, "bbb", string(data))
}

// export runs Export on records and returns the error of each, by pathname.
func export(t *testing.T, exp kexporter.Exporter, records ...*connectors.Record) map[string]error {
	t.Helper()
	in := make(chan *connectors.Record, len(records))
	out := make(chan *connectors.Result, len(records))
	for _, r := range records {
		in <- r
	}
	close(in)
	require.NoError(t, exp.Export(context.Background(), in, out))
	got := make(map[string]error)
	for r := range out {
		got[r.Record.Pathname] = r.Err
	}
	return got
}

func TestExportUnsupported(t *testing.T) {
	addr, dst := startServer(t, testFS())
	exp := newExporter(t, "rsync://"+addr+"/dst")

	open := func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("")), nil }
	fifo := connectors.NewRecord("/fifo", "", objects.FileInfo{Lname: "fifo", Lmode: fs.ModeNamedPipe | 0o644, LmodTime: mtime}, nil, open)
	xattr := connectors.NewXattr("/a.txt", "user.test", objects.AttributeExtended, open)

	got := export(t, exp, fifo, xattr)
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
	got := export(t, exp, connectors.NewRecord("/new.txt", "", objects.FileInfo{Lname: "new.txt", Lsize: 3, Lmode: 0o644, LmodTime: mtime}, nil, open))
	require.Error(t, got["/new.txt"])
}
