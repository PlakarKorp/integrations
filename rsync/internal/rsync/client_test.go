package rsync_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"maps"
	"net"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"github.com/PlakarKorp/integrations/rsync/internal/rsync"
	"github.com/gokrazy/rsync/rsyncd"
	"github.com/stretchr/testify/require"
)

func startServer(t *testing.T, fsys fs.FS) string {
	t.Helper()
	srv, err := rsyncd.NewServer([]rsyncd.Module{{Name: "mod", FS: fsys}}, rsyncd.DontRestrict())
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
	})
	return ln.Addr().String()
}

var mtime = time.Unix(1700000000, 0)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"hello.txt":       {Data: []byte("hello"), Mode: 0o644, ModTime: mtime},
		"dir":             {Mode: fs.ModeDir | 0o750, ModTime: mtime},
		"dir/big.bin":     {Data: bytes.Repeat([]byte("0123456789"), 100_000), Mode: 0o600, ModTime: mtime},
		"dir/sub":         {Mode: fs.ModeDir | 0o755, ModTime: mtime},
		"dir/sub/x":       {Data: []byte("x"), Mode: 0o644, ModTime: mtime},
		"dir/sub/symlink": {Data: []byte("../big.bin"), Mode: fs.ModeSymlink | 0o777, ModTime: mtime},
	}
}

func TestList(t *testing.T) {
	c := rsync.NewClient(startServer(t, testFS()), "mod", "", "", 1)

	files, _, err := c.List(context.Background(), "")
	require.NoError(t, err)

	got := map[string]rsync.File{}
	for _, f := range files {
		got[f.Name] = f
	}
	require.ElementsMatch(t,
		[]string{".", "hello.txt", "dir", "dir/big.bin", "dir/sub", "dir/sub/x", "dir/sub/symlink"},
		slices.Collect(maps.Keys(got)))

	require.Equal(t, int64(5), got["hello.txt"].Size)
	require.Equal(t, fs.FileMode(0o644), got["hello.txt"].Mode)
	require.True(t, got["hello.txt"].ModTime.Equal(mtime))
	require.Equal(t, fs.ModeDir|0o750, got["dir"].Mode)
	require.Equal(t, int64(1_000_000), got["dir/big.bin"].Size)
	require.Equal(t, fs.ModeSymlink, got["dir/sub/symlink"].Mode.Type())
	require.Equal(t, "../big.bin", got["dir/sub/symlink"].LinkTarget)
	require.Equal(t, "0", got["hello.txt"].User)
}

func TestListSubdirectory(t *testing.T) {
	c := rsync.NewClient(startServer(t, testFS()), "mod", "", "", 1)

	files, _, err := c.List(context.Background(), "dir/sub")
	require.NoError(t, err)

	var names []string
	for _, f := range files {
		names = append(names, f.Name)
	}
	require.ElementsMatch(t, []string{".", "x", "symlink"}, names)
}

func TestOpen(t *testing.T) {
	fsys := testFS()
	c := rsync.NewClient(startServer(t, fsys), "mod", "", "", 1)

	for _, name := range []string{"hello.txt", "dir/big.bin", "dir/sub/x"} {
		rd, err := c.Open(context.Background(), name)
		require.NoError(t, err, name)
		data, err := io.ReadAll(rd)
		require.NoError(t, err, name)
		require.NoError(t, rd.Close())
		require.Equal(t, fsys[name].Data, data, name)
	}
}

func TestOpenMissing(t *testing.T) {
	c := rsync.NewClient(startServer(t, testFS()), "mod", "", "", 1)

	_, err := c.Open(context.Background(), "nope")
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestOpenDirectory(t *testing.T) {
	c := rsync.NewClient(startServer(t, testFS()), "mod", "", "", 1)

	_, err := c.Open(context.Background(), "dir")
	require.Error(t, err)
}

func TestNewlineInPath(t *testing.T) {
	c := rsync.NewClient(startServer(t, testFS()), "mod", "", "", 1)

	_, _, err := c.List(context.Background(), "dir\n--delete")
	require.ErrorContains(t, err, "newline")
	_, err = c.Open(context.Background(), "dir\n--delete")
	require.ErrorContains(t, err, "newline")
}

// fakeServer serves a single connection, sending a file list with one regular
// file named name. It lets tests send what a real server would not.
func fakeServer(t *testing.T, name string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	le := binary.LittleEndian
	var flist []byte
	flist = append(flist, 1<<6) // XMIT_LONG_NAME
	flist = le.AppendUint32(flist, uint32(len(name)))
	flist = append(flist, name...)
	for _, v := range []uint32{3, 1700000000, 0o100644, 0, 0} { // size, mtime, mode, uid, gid
		flist = le.AppendUint32(flist, v)
	}
	flist = append(flist, 0)          // end of list
	flist = le.AppendUint32(flist, 0) // users
	flist = le.AppendUint32(flist, 0) // groups
	flist = le.AppendUint32(flist, 0) // I/O errors
	frame := le.AppendUint32(nil, 7<<24|uint32(len(flist)))

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		rd := bufio.NewReader(conn)
		_, _ = rd.ReadString('\n') // greeting
		_, _ = io.WriteString(conn, "@RSYNCD: 27\n")
		_, _ = rd.ReadString('\n') // module
		_, _ = io.WriteString(conn, "@RSYNCD: OK\n")
		for { // arguments
			if line, err := rd.ReadString('\n'); err != nil || line == "\n" {
				break
			}
		}
		_, _ = conn.Write(le.AppendUint32(nil, 0)) // seed
		_, _ = io.ReadFull(rd, make([]byte, 4))    // exclusion list
		_, _ = conn.Write(append(frame, flist...))
	}()
	return ln.Addr().String()
}

func TestListRejectsNamesOutside(t *testing.T) {
	for _, name := range []string{"../escape", "a/../../escape", "/etc/passwd", ".."} {
		c := rsync.NewClient(fakeServer(t, name), "mod", "", "", 1)
		_, _, err := c.List(context.Background(), "")
		require.ErrorContains(t, err, "outside the requested directory", name)
	}
}

func TestUnknownModule(t *testing.T) {
	c := rsync.NewClient(startServer(t, testFS()), "nope", "", "", 1)

	_, _, err := c.List(context.Background(), "")
	require.Error(t, err)
}

func TestMaxConnections(t *testing.T) {
	c := rsync.NewClient(startServer(t, testFS()), "mod", "", "", 1)

	rd, err := c.Open(context.Background(), "hello.txt")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = c.Open(ctx, "hello.txt")
	require.True(t, errors.Is(err, context.DeadlineExceeded), err)

	require.NoError(t, rd.Close())
	rd, err = c.Open(context.Background(), "hello.txt")
	require.NoError(t, err)
	require.NoError(t, rd.Close())
}
