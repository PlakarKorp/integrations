package rsynctest

import (
	"context"
	"io"
	"io/fs"
	"maps"
	"net"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/gokrazy/rsync/rsyncd"
	"github.com/stretchr/testify/require"
)

// StartServer serves modules with gokrazy's in-process daemon and returns its
// address.
func StartServer(t *testing.T, modules ...rsyncd.Module) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	Serve(t, ln, modules...)
	return ln.Addr().String()
}

// Serve serves modules on ln until the test ends.
func Serve(t *testing.T, ln net.Listener, modules ...rsyncd.Module) {
	t.Helper()
	srv, err := rsyncd.NewServer(modules, rsyncd.DontRestrict())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
	})
}

// Import runs imp's Import to the end and returns its records by pathname.
// Their content is left unread: plakar reads it once the listing is done, and
// only for the files it does not have yet.
func Import(t *testing.T, imp integration.Importer) map[string]*integration.Record {
	t.Helper()
	records := make(chan *integration.Record)
	errc := make(chan error, 1)
	go func() {
		_, err := imp.Import(context.Background(), records, nil)
		errc <- err
	}()
	got := make(map[string]*integration.Record)
	for r := range records {
		got[r.Pathname] = r
	}
	require.NoError(t, <-errc)
	return got
}

// Read reads the content of r and closes it.
func Read(t *testing.T, r *integration.Record) string {
	t.Helper()
	data, err := io.ReadAll(r.Reader)
	require.NoError(t, err, r.Pathname)
	require.NoError(t, r.Close(), r.Pathname)
	return string(data)
}

// Entry is an imported record with its content.
type Entry struct {
	Info   integration.FileInfo
	Target string
	Data   string
}

// Backup imports everything imp yields, with the content of regular files.
func Backup(t *testing.T, imp integration.Importer) map[string]Entry {
	t.Helper()
	backup := make(map[string]Entry)
	for name, r := range Import(t, imp) {
		require.NoError(t, r.Err, name)
		e := Entry{Info: r.FileInfo, Target: r.Target}
		if r.FileInfo.Mode().IsRegular() {
			e.Data = Read(t, r)
		}
		backup[name] = e
	}
	return backup
}

// Restore returns the records plakar sends an exporter to restore pathname
// from backup: the entries below it, parents first, with paths relative to
// pathname. A file is relative to its directory, and comes after a record
// for the root.
func Restore(backup map[string]Entry, pathname string) []*integration.Record {
	var records []*integration.Record
	names := slices.Sorted(maps.Keys(backup))
	strip := pathname
	if !backup[pathname].Info.IsDir() {
		names = []string{pathname}
		strip = path.Dir(pathname)
		root := integration.FileInfo{Lname: "/", Lmode: fs.ModeDir | 0o700, LmodTime: time.Now()}
		records = append(records, integration.NewRecord("/", "", root, nil, nil))
	}
	for _, name := range names {
		if name != pathname && !strings.HasPrefix(name, pathname+"/") {
			continue
		}
		e := backup[name]
		rel := path.Join("/", strings.TrimPrefix(name, strip))
		records = append(records, integration.NewRecord(rel, e.Target, e.Info, nil, func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(e.Data)), nil
		}))
	}
	return records
}

// Export runs exp's Export on records and returns the error of each, by
// pathname.
func Export(t *testing.T, exp integration.Exporter, records ...*integration.Record) map[string]error {
	t.Helper()
	in := make(chan *integration.Record, len(records))
	out := make(chan *integration.Result, len(records))
	for _, r := range records {
		in <- r
	}
	close(in)
	require.NoError(t, exp.Export(context.Background(), nil, in, out))
	got := make(map[string]error)
	for r := range out {
		got[r.Record.Pathname] = r.Err
	}
	return got
}
