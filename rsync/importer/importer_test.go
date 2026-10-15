package importer_test

import (
	"context"
	"io"
	"io/fs"
	"maps"
	"net"
	"slices"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/PlakarKorp/integrations/rsync/importer"
	"github.com/PlakarKorp/kloset/connectors"
	kimporter "github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/repository"
	"github.com/PlakarKorp/kloset/snapshot"
	ktest "github.com/PlakarKorp/kloset/testing"
	"github.com/gokrazy/rsync/rsyncd"
	"github.com/stretchr/testify/require"
)

// countingListener counts the connections made to the server.
type countingListener struct {
	net.Listener
	conns atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.conns.Add(1)
	}
	return c, err
}

func startServer(t *testing.T, fsys fs.FS) (string, *countingListener) {
	t.Helper()
	srv, err := rsyncd.NewServer([]rsyncd.Module{{Name: "mod", FS: fsys}}, rsyncd.DontRestrict())
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	cl := &countingListener{Listener: ln}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx, cl) }()
	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
	})
	return ln.Addr().String(), cl
}

var mtime = time.Unix(1700000000, 0)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"a.txt":      {Data: []byte("aaa"), Mode: 0o644, ModTime: mtime},
		"dir":        {Mode: fs.ModeDir | 0o755, ModTime: mtime},
		"dir/b.txt":  {Data: []byte("bbb"), Mode: 0o600, ModTime: mtime},
		"dir/c.txt":  {Data: []byte("ccc"), Mode: 0o644, ModTime: mtime},
		"dir/link":   {Data: []byte("b.txt"), Mode: fs.ModeSymlink | 0o777, ModTime: mtime},
		"other/d.md": {Data: []byte("ddd"), Mode: 0o644, ModTime: mtime},
	}
}

func newImporter(t *testing.T, config map[string]string) kimporter.Importer {
	t.Helper()
	imp, err := importer.NewImporter(context.Background(), &connectors.Options{}, "rsync", config)
	require.NoError(t, err)
	return imp
}

func TestNewImporter(t *testing.T) {
	for _, tc := range []struct {
		location string
		config   map[string]string
		root     string
		origin   string
	}{
		{location: "rsync://host/mod", root: "/mod", origin: "host"},
		{location: "rsync://host/mod/", root: "/mod", origin: "host"},
		{location: "rsync://user@host:8873/mod/a/b/", root: "/mod/a/b", origin: "host"},
		{location: "rsync://host/mod/a", config: map[string]string{"port": "8873", "max_connections": "1"}, root: "/mod/a", origin: "host"},
		{location: "rsync://host", config: map[string]string{"module": "mod"}, root: "/mod", origin: "host"},
		{location: "rsync://host/", config: map[string]string{"module": "mod"}, root: "/mod", origin: "host"},
		{location: "rsync://host/mod", config: map[string]string{"root": "a/b"}, root: "/mod/a/b", origin: "host"},
		{location: "rsync://host/mod/a", config: map[string]string{"root": "/b/"}, root: "/mod/a/b", origin: "host"},
		{location: "rsync://host", config: map[string]string{"module": "mod", "root": "a/b"}, root: "/mod/a/b", origin: "host"},
		{location: "rsync://host/mod/a", config: map[string]string{"root": "../../.."}, root: "/mod", origin: "host"},
	} {
		config := map[string]string{"location": tc.location}
		for k, v := range tc.config {
			config[k] = v
		}
		imp := newImporter(t, config)
		require.Equal(t, tc.root, imp.Root(), tc.location)
		require.Equal(t, tc.origin, imp.Origin(), tc.location)
		require.Equal(t, "rsync", imp.Type())
	}

	for _, config := range []map[string]string{
		{"location": "rsync://host"},
		{"location": "rsync://host/"},
		{"location": "rsync://user@host/mod", "username": "other"},
		{"location": "rsync://user:pass@host/mod", "password": "other"},
		{"location": "rsync://host/mod", "module": "other"},
		{"location": "rsync://host", "module": ""},
		{"location": "rsync://host", "module": "a/b"},
		{"location": "rsync://host/mod", "max_connections": "0"},
		{"location": "rsync://host/mod", "max_connections": "many"},
	} {
		_, err := importer.NewImporter(context.Background(), &connectors.Options{}, "rsync", config)
		require.Error(t, err, config)
	}
}

func TestNewImporterErrorsHidePassword(t *testing.T) {
	for _, location := range []string{
		"rsync://user:hunter2@host",
		"rsync://user:hunter2@host:badport/mod",
	} {
		_, err := importer.NewImporter(context.Background(), &connectors.Options{}, "rsync", map[string]string{"location": location})
		require.Error(t, err, location)
		require.NotContains(t, err.Error(), "hunter2", location)
	}
}

func importAll(t *testing.T, imp kimporter.Importer) map[string]*connectors.Record {
	t.Helper()
	records := make(chan *connectors.Record)
	errc := make(chan error, 1)
	go func() { errc <- imp.Import(context.Background(), records, nil) }()
	got := map[string]*connectors.Record{}
	for r := range records {
		got[r.Pathname] = r
	}
	require.NoError(t, <-errc)
	return got
}

func TestImport(t *testing.T) {
	addr, _ := startServer(t, testFS())
	imp := newImporter(t, map[string]string{"location": "rsync://" + addr + "/mod/dir"})
	require.NoError(t, imp.Ping(context.Background()))

	got := importAll(t, imp)
	require.ElementsMatch(t,
		[]string{"/mod/dir", "/mod/dir/b.txt", "/mod/dir/c.txt", "/mod/dir/link"},
		slices.Collect(maps.Keys(got)))

	dir := got["/mod/dir"].FileInfo
	require.Equal(t, "dir", dir.Name())
	require.True(t, dir.IsDir())

	link := got["/mod/dir/link"]
	require.Equal(t, "b.txt", link.Target)
	require.Equal(t, fs.ModeSymlink, link.FileInfo.Mode().Type())

	b := got["/mod/dir/b.txt"]
	require.Nil(t, b.Err)
	require.Equal(t, "b.txt", b.FileInfo.Name())
	require.Equal(t, int64(3), b.FileInfo.Size())
	require.Equal(t, fs.FileMode(0o600), b.FileInfo.Mode())
	require.True(t, b.FileInfo.ModTime().Equal(mtime))
	data, err := io.ReadAll(b.Reader)
	require.NoError(t, err)
	require.Equal(t, "bbb", string(data))

	for _, r := range got {
		require.NoError(t, r.Close())
	}
}

func TestImportRoot(t *testing.T) {
	addr, _ := startServer(t, testFS())
	imp := newImporter(t, map[string]string{"location": "rsync://" + addr, "module": "mod", "root": "dir"})

	got := importAll(t, imp)
	require.ElementsMatch(t,
		[]string{"/mod/dir", "/mod/dir/b.txt", "/mod/dir/c.txt", "/mod/dir/link"},
		slices.Collect(maps.Keys(got)))
	data, err := io.ReadAll(got["/mod/dir/b.txt"].Reader)
	require.NoError(t, err)
	require.Equal(t, "bbb", string(data))
	require.NoError(t, imp.Close(context.Background()))
}

// In plugin mode, the context given to NewImporter is the Init call's,
// canceled before any content is read.
func TestImportAfterInitContextCanceled(t *testing.T) {
	addr, _ := startServer(t, testFS())
	ctx, cancel := context.WithCancel(context.Background())
	imp, err := importer.NewImporter(ctx, &connectors.Options{}, "rsync", map[string]string{"location": "rsync://" + addr + "/mod"})
	require.NoError(t, err)
	cancel()

	got := importAll(t, imp)
	data, err := io.ReadAll(got["/mod/a.txt"].Reader)
	require.NoError(t, err)
	require.Equal(t, "aaa", string(data))
	require.NoError(t, imp.Close(context.Background()))
}

func TestImportUnknownModule(t *testing.T) {
	addr, _ := startServer(t, testFS())
	imp := newImporter(t, map[string]string{"location": "rsync://" + addr + "/nope"})
	require.Error(t, imp.Ping(context.Background()))

	records := make(chan *connectors.Record)
	go func() {
		for range records {
		}
	}()
	require.Error(t, imp.Import(context.Background(), records, nil))
}

func TestImportUnsupportedType(t *testing.T) {
	fsys := testFS()
	fsys["dir/fifo"] = &fstest.MapFile{Mode: fs.ModeNamedPipe | 0o644, ModTime: mtime}
	addr, _ := startServer(t, fsys)
	imp := newImporter(t, map[string]string{"location": "rsync://" + addr + "/mod/dir"})

	got := importAll(t, imp)
	require.Error(t, got["/mod/dir/fifo"].Err)
	require.Nil(t, got["/mod/dir/b.txt"].Err)
}

func backup(t *testing.T, repo *repository.Repository, imp kimporter.Importer, parent *snapshot.Snapshot) *snapshot.Snapshot {
	t.Helper()
	builder, err := snapshot.Create(repo, repository.DefaultType, "", objects.NilMac, &snapshot.BuilderOptions{Name: "test"})
	require.NoError(t, err)
	if parent != nil {
		vfs, err := parent.FilesystemWithCache()
		require.NoError(t, err)
		builder.WithVFSCache(vfs)
	}
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

func readFile(t *testing.T, snap *snapshot.Snapshot, name string) string {
	t.Helper()
	vfs, err := snap.Filesystem()
	require.NoError(t, err)
	f, err := vfs.Open(name)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	require.NoError(t, err)
	return string(data)
}

// The point of the integration: a backup made with the previous snapshot as
// parent fetches only the files that changed since.
func TestIncrementalBackup(t *testing.T) {
	fsys := testFS()
	addr, ln := startServer(t, fsys)
	repo := ktest.GenerateRepository(t, nil, nil, nil)
	imp := newImporter(t, map[string]string{"location": "rsync://" + addr + "/mod"})

	// One connection for the listing, one per regular file.
	first := backup(t, repo, imp, nil)
	require.Equal(t, int64(1+4), ln.conns.Load())
	require.Equal(t, "bbb", readFile(t, first, "/mod/dir/b.txt"))

	ln.conns.Store(0)
	second := backup(t, repo, imp, first)
	require.Equal(t, int64(1), ln.conns.Load())
	require.Equal(t, "bbb", readFile(t, second, "/mod/dir/b.txt"))

	fsys["dir/b.txt"] = &fstest.MapFile{Data: []byte("changed"), Mode: 0o600, ModTime: mtime.Add(time.Hour)}
	fsys["new.txt"] = &fstest.MapFile{Data: []byte("new"), Mode: 0o644, ModTime: mtime}
	ln.conns.Store(0)
	third := backup(t, repo, imp, second)
	require.Equal(t, int64(1+2), ln.conns.Load())
	require.Equal(t, "changed", readFile(t, third, "/mod/dir/b.txt"))
	require.Equal(t, "new", readFile(t, third, "/mod/new.txt"))
	require.Equal(t, "ccc", readFile(t, third, "/mod/dir/c.txt"))
}
