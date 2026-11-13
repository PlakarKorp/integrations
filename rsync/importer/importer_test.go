package importer_test

import (
	"context"
	"io/fs"
	"maps"
	"net"
	"slices"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integrations/rsync/importer"
	"github.com/PlakarKorp/integrations/rsync/internal/rsynctest"
	"github.com/gokrazy/rsync/rsyncd"
	"github.com/stretchr/testify/require"
)

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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	cl := &countingListener{Listener: ln}
	rsynctest.Serve(t, cl, rsyncd.Module{Name: "mod", FS: fsys})
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

func newImporter(t *testing.T, config map[string]string) integration.Importer {
	t.Helper()
	imp, err := importer.NewImporter(context.Background(), &integration.Options{}, "rsync", config)
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
		// plakman sets the port both in the location and as a parameter.
		{location: "rsync://host:8873/mod", config: map[string]string{"port": "8873"}, root: "/mod", origin: "host"},
		{location: "rsync://user:pass@host/mod", config: map[string]string{"username": "user", "password": "pass"}, root: "/mod", origin: "host"},
		{location: "rsync://host/mod", config: map[string]string{"module": "mod"}, root: "/mod", origin: "host"},
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
		{"location": "rsync://host:8873/mod", "port": "873"},
		{"location": "rsync://host/mod", "module": "other"},
		{"location": "rsync://host", "module": ""},
		{"location": "rsync://host", "module": "a/b"},
		{"location": "rsync://host/mod", "max_connections": "0"},
		{"location": "rsync://host/mod", "max_connections": "many"},
	} {
		_, err := importer.NewImporter(context.Background(), &integration.Options{}, "rsync", config)
		require.Error(t, err, config)
	}
}

func TestNewImporterErrorsHidePassword(t *testing.T) {
	for _, location := range []string{
		"rsync://user:hunter2@host",
		"rsync://user:hunter2@host:badport/mod",
	} {
		_, err := importer.NewImporter(context.Background(), &integration.Options{}, "rsync", map[string]string{"location": location})
		require.Error(t, err, location)
		require.NotContains(t, err.Error(), "hunter2", location)
	}
}

func TestImport(t *testing.T) {
	addr, _ := startServer(t, testFS())
	imp := newImporter(t, map[string]string{"location": "rsync://" + addr + "/mod/dir"})
	require.NoError(t, imp.Ping(context.Background()))

	got := rsynctest.Import(t, imp)
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
	require.NoError(t, b.Err)
	require.Equal(t, "b.txt", b.FileInfo.Name())
	require.Equal(t, int64(3), b.FileInfo.Size())
	require.Equal(t, fs.FileMode(0o600), b.FileInfo.Mode())
	require.True(t, b.FileInfo.ModTime().Equal(mtime))
	require.Equal(t, "bbb", rsynctest.Read(t, b))

	for _, r := range got {
		require.NoError(t, r.Close())
	}
}

func TestImportFile(t *testing.T) {
	addr, _ := startServer(t, testFS())
	imp := newImporter(t, map[string]string{"location": "rsync://" + addr + "/mod/dir/b.txt"})
	require.NoError(t, imp.Ping(context.Background()))

	got := rsynctest.Import(t, imp)
	require.Equal(t, []string{"/mod/dir/b.txt"}, slices.Collect(maps.Keys(got)))
	b := got["/mod/dir/b.txt"]
	require.Equal(t, "b.txt", b.FileInfo.Name())
	require.Equal(t, "bbb", rsynctest.Read(t, b))
	require.NoError(t, imp.Close(context.Background()))
}

func TestImportRoot(t *testing.T) {
	addr, _ := startServer(t, testFS())
	imp := newImporter(t, map[string]string{"location": "rsync://" + addr, "module": "mod", "root": "dir"})

	got := rsynctest.Import(t, imp)
	require.ElementsMatch(t,
		[]string{"/mod/dir", "/mod/dir/b.txt", "/mod/dir/c.txt", "/mod/dir/link"},
		slices.Collect(maps.Keys(got)))
	require.Equal(t, "bbb", rsynctest.Read(t, got["/mod/dir/b.txt"]))
	require.NoError(t, imp.Close(context.Background()))
}

// In plugin mode, the context given to NewImporter is the Init call's,
// canceled before any content is read.
func TestImportAfterInitContextCanceled(t *testing.T) {
	addr, _ := startServer(t, testFS())
	ctx, cancel := context.WithCancel(context.Background())
	imp, err := importer.NewImporter(ctx, &integration.Options{}, "rsync", map[string]string{"location": "rsync://" + addr + "/mod"})
	require.NoError(t, err)
	cancel()

	got := rsynctest.Import(t, imp)
	require.Equal(t, "aaa", rsynctest.Read(t, got["/mod/a.txt"]))
	require.NoError(t, imp.Close(context.Background()))
}

func TestImportUnknownModule(t *testing.T) {
	addr, _ := startServer(t, testFS())
	imp := newImporter(t, map[string]string{"location": "rsync://" + addr + "/nope"})
	require.Error(t, imp.Ping(context.Background()))

	records := make(chan *integration.Record)
	go func() {
		for range records {
		}
	}()
	_, err := imp.Import(context.Background(), records, nil)
	require.Error(t, err)
}

func TestImportUnsupportedType(t *testing.T) {
	fsys := testFS()
	fsys["dir/fifo"] = &fstest.MapFile{Mode: fs.ModeNamedPipe | 0o644, ModTime: mtime}
	addr, _ := startServer(t, fsys)
	imp := newImporter(t, map[string]string{"location": "rsync://" + addr + "/mod/dir"})

	got := rsynctest.Import(t, imp)
	require.Error(t, got["/mod/dir/fifo"].Err)
	require.NoError(t, got["/mod/dir/b.txt"].Err)
}

// backup imports from imp what plakar would back up after prev, the records
// of the previous backup: it reads the content of the regular files whose
// metadata changed, or that are new. It returns the records and the paths
// read, and closes the records left unread.
func backup(t *testing.T, imp integration.Importer, prev map[string]*integration.Record) (map[string]*integration.Record, []string) {
	t.Helper()
	got := rsynctest.Import(t, imp)
	var read []string
	for name, r := range got {
		require.NoError(t, r.Err, name)
		if old, ok := prev[name]; ok && old.FileInfo.Equal(&r.FileInfo) && old.Target == r.Target {
			require.NoError(t, r.Close())
			continue
		}
		if r.FileInfo.Mode().IsRegular() {
			read = append(read, name)
		}
	}
	return got, read
}

// The point of the integration: a backup made with the previous snapshot as
// parent fetches only the files that changed since. The importer must report
// the same metadata for unchanged files, and open no connection for the
// content plakar does not read.
func TestIncrementalBackup(t *testing.T) {
	fsys := testFS()
	addr, ln := startServer(t, fsys)
	imp := newImporter(t, map[string]string{"location": "rsync://" + addr + "/mod"})
	content := map[string]string{}
	fetch := func(got map[string]*integration.Record, read []string) {
		for _, name := range read {
			content[name] = rsynctest.Read(t, got[name])
		}
	}

	// One connection for the listing, one per regular file.
	first, read := backup(t, imp, nil)
	require.ElementsMatch(t, []string{"/mod/a.txt", "/mod/dir/b.txt", "/mod/dir/c.txt", "/mod/other/d.md"}, read)
	fetch(first, read)
	require.Equal(t, int64(1+4), ln.conns.Load())
	require.Equal(t, "bbb", content["/mod/dir/b.txt"])

	ln.conns.Store(0)
	second, read := backup(t, imp, first)
	require.Empty(t, read)
	require.Len(t, second, len(first))
	require.Equal(t, int64(1), ln.conns.Load())

	fsys["dir/b.txt"] = &fstest.MapFile{Data: []byte("changed"), Mode: 0o600, ModTime: mtime.Add(time.Hour)}
	fsys["new.txt"] = &fstest.MapFile{Data: []byte("new"), Mode: 0o644, ModTime: mtime}
	ln.conns.Store(0)
	third, read := backup(t, imp, second)
	require.ElementsMatch(t, []string{"/mod/dir/b.txt", "/mod/new.txt"}, read)
	fetch(third, read)
	require.Equal(t, int64(1+2), ln.conns.Load())
	require.Equal(t, "changed", content["/mod/dir/b.txt"])
	require.Equal(t, "new", content["/mod/new.txt"])
	require.Equal(t, "ccc", content["/mod/dir/c.txt"])
	require.NoError(t, imp.Close(context.Background()))
}
