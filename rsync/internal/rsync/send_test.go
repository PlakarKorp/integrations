package rsync_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/PlakarKorp/integrations/rsync/internal/rsync"
	"github.com/PlakarKorp/integrations/rsync/internal/rsynctest"
	"github.com/gokrazy/rsync/rsyncd"
	"github.com/stretchr/testify/require"
)

// startWritableServer serves a writable module "mod" from a temporary
// directory, which it returns.
func startWritableServer(t *testing.T) (string, string) {
	dir := t.TempDir()
	return rsynctest.StartServer(t, rsyncd.Module{Name: "mod", Path: dir, Writable: true}), dir
}

func content(data []byte) func(rsync.File) (io.Reader, error) {
	return func(rsync.File) (io.Reader, error) {
		return bytes.NewReader(data), nil
	}
}

func TestSend(t *testing.T) {
	addr, dir := startWritableServer(t)
	c := rsync.NewClient(addr, "mod", "", "", 1)
	ctx := context.Background()
	big := bytes.Repeat([]byte("0123456789"), 100_000)

	require.NoError(t, c.Send(ctx, "", []rsync.File{{Name: "d", Mode: fs.ModeDir | 0o750, ModTime: mtime}}, nil))
	require.NoError(t, c.Send(ctx, "d", []rsync.File{{Name: "f", Size: 5, Mode: 0o640, ModTime: mtime}}, content([]byte("hello"))))
	require.NoError(t, c.Send(ctx, "d", []rsync.File{{Name: "big", Size: int64(len(big)), Mode: 0o600, ModTime: mtime}}, content(big)))
	require.NoError(t, c.Send(ctx, "", []rsync.File{{Name: "l", Mode: fs.ModeSymlink | 0o777, LinkTarget: "d/f"}}, nil))
	// Writing into d changed its time: send it again to restore it.
	require.NoError(t, c.Send(ctx, "", []rsync.File{{Name: "d", Mode: fs.ModeDir | 0o750, ModTime: mtime}}, nil))

	data, err := os.ReadFile(filepath.Join(dir, "d", "f"))
	require.NoError(t, err)
	require.Equal(t, "hello", string(data))
	data, err = os.ReadFile(filepath.Join(dir, "d", "big"))
	require.NoError(t, err)
	require.Equal(t, big, data)

	for name, mode := range map[string]fs.FileMode{"d": fs.ModeDir | 0o750, "d/f": 0o640, "d/big": 0o600} {
		fi, err := os.Stat(filepath.Join(dir, name))
		require.NoError(t, err)
		require.Equal(t, mode, fi.Mode(), name)
		require.True(t, fi.ModTime().Equal(mtime), name)
	}
	target, err := os.Readlink(filepath.Join(dir, "l"))
	require.NoError(t, err)
	require.Equal(t, "d/f", target)
}

func TestSendOverwrites(t *testing.T) {
	addr, dir := startWritableServer(t)
	c := rsync.NewClient(addr, "mod", "", "", 1)
	f := rsync.File{Name: "f", Size: 3, Mode: 0o644, ModTime: mtime}

	require.NoError(t, c.Send(context.Background(), "", []rsync.File{f}, content([]byte("old"))))
	// Same size and time: only the content differs.
	require.NoError(t, c.Send(context.Background(), "", []rsync.File{f}, content([]byte("new"))))

	data, err := os.ReadFile(filepath.Join(dir, "f"))
	require.NoError(t, err)
	require.Equal(t, "new", string(data))
}

func TestSendOpenError(t *testing.T) {
	addr, dir := startWritableServer(t)
	c := rsync.NewClient(addr, "mod", "", "", 1)
	errOpen := errors.New("cannot open")

	err := c.Send(context.Background(), "", []rsync.File{{Name: "f", Size: 3, Mode: 0o644, ModTime: mtime}},
		func(rsync.File) (io.Reader, error) { return nil, errOpen })
	require.ErrorIs(t, err, errOpen)
	_, err = os.Stat(filepath.Join(dir, "f"))
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestSendReadOnly(t *testing.T) {
	c := rsync.NewClient(startServer(t, testFS()), "mod", "", "", 1)

	err := c.Send(context.Background(), "", []rsync.File{{Name: "f", Size: 3, Mode: 0o644, ModTime: mtime}}, content([]byte("abc")))
	require.Error(t, err)
}

// The receiver reports a file it could not write, then ends the transfer as
// usual.
func TestSendServerError(t *testing.T) {
	addr := fakeServer(t,
		errorFrame("rsync: mkstemp \"x\" failed: Permission denied (13)\n"),
		dataFrame(int32s(0xffffffff, 0xffffffff, 0xffffffff)), // end of both phases, goodbye
	)
	c := rsync.NewClient(addr, "mod", "", "", 1)

	err := c.Send(context.Background(), "", []rsync.File{{Name: "x", Size: 3, Mode: 0o644, ModTime: mtime}}, content([]byte("abc")))
	require.ErrorContains(t, err, "Permission denied")
}
