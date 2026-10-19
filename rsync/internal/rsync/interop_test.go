package rsync_test

import (
	"context"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/integrations/rsync/internal/rsync"
	"github.com/PlakarKorp/integrations/rsync/internal/rsynctest"
	"github.com/stretchr/testify/require"
)

func TestInterop(t *testing.T) {
	ctx := context.Background()
	addr := rsynctest.StartRsyncd(t)

	for _, tc := range []struct{ module, user, password, owner, root string }{
		{"pub", "", "", "alice", "root"},
		{"priv", "bob", "secret", "1234", "0"},
	} {
		t.Run(tc.module, func(t *testing.T) {
			c := rsync.NewClient(addr, tc.module, tc.user, tc.password, 2)

			files, _, err := c.List(ctx, "")
			require.NoError(t, err)
			got := map[string]rsync.File{}
			for _, f := range files {
				got[f.Name] = f
			}
			require.Equal(t, tc.owner, got["hello.txt"].User)
			require.Equal(t, tc.root, got["dir/big.bin"].User)
			require.Equal(t, tc.root, got["dir/big.bin"].Group)
			require.Equal(t, "../big.bin", got["dir/sub/symlink"].LinkTarget)
			require.Equal(t, int64(3_000_000), got["dir/big.bin"].Size)

			for name, want := range map[string]string{
				"hello.txt": "hello\n",
				"a*b":       "star\n",
				`a\b`:       "backslash\n",
				"glob[1]/f": "bracket\n",
			} {
				rd, err := c.Open(ctx, name)
				require.NoError(t, err, name)
				data, err := io.ReadAll(rd)
				require.NoError(t, err, name)
				require.NoError(t, rd.Close())
				require.Equal(t, want, string(data), name)
			}

			rd, err := c.Open(ctx, "dir/big.bin")
			require.NoError(t, err)
			n, err := io.Copy(io.Discard, rd)
			require.NoError(t, err)
			require.NoError(t, rd.Close())
			require.Equal(t, int64(3_000_000), n)
		})
	}

	// The daemon expands wildcards in requested paths.
	t.Run("wildcard directory", func(t *testing.T) {
		c := rsync.NewClient(addr, "pub", "", "", 1)
		files, _, err := c.List(ctx, "glob[1]")
		require.NoError(t, err)
		var names []string
		for _, f := range files {
			names = append(names, f.Name)
		}
		require.ElementsMatch(t, []string{".", "f"}, names)

		rd, err := c.Open(ctx, "glob[1]/f")
		require.NoError(t, err)
		data, err := io.ReadAll(rd)
		require.NoError(t, err)
		require.NoError(t, rd.Close())
		require.Equal(t, "bracket\n", string(data))
	})

	t.Run("single file and missing path", func(t *testing.T) {
		c := rsync.NewClient(addr, "pub", "", "", 1)
		files, _, err := c.List(ctx, "dir/big.bin")
		require.NoError(t, err)
		require.Len(t, files, 1)
		require.Equal(t, ".", files[0].Name)
		require.Equal(t, int64(3_000_000), files[0].Size)

		_, _, err = c.List(ctx, "nope")
		require.ErrorIs(t, err, fs.ErrNotExist)

		require.NoError(t, c.Ping(ctx, "dir"))
		require.NoError(t, c.Ping(ctx, "hello.txt"))
		require.Error(t, c.Ping(ctx, "nope"))
	})

	// The daemon reports a file it cannot write, and ends the transfer.
	t.Run("send error", func(t *testing.T) {
		c := rsync.NewClient(addr, "restore", "", "", 1)
		dir := rsync.File{Mode: fs.ModeDir | 0o755, ModTime: time.Unix(1700000000, 0)}
		file := rsync.File{Size: 3, Mode: 0o644, ModTime: time.Unix(1700000000, 0)}
		open := func(rsync.File) (io.Reader, error) { return strings.NewReader("abc"), nil }

		dir.Name = "senderr"
		require.NoError(t, c.Send(ctx, "", []rsync.File{dir}, nil))
		dir.Name = "x"
		require.NoError(t, c.Send(ctx, "senderr", []rsync.File{dir}, nil))
		file.Name = "y"
		require.NoError(t, c.Send(ctx, "senderr/x", []rsync.File{file}, open))
		// A non-empty directory stands where the file should go.
		file.Name = "x"
		require.Error(t, c.Send(ctx, "senderr", []rsync.File{file}, open))
	})

	t.Run("bad password", func(t *testing.T) {
		c := rsync.NewClient(addr, "priv", "bob", "wrong", 1)
		_, _, err := c.List(ctx, "")
		require.Error(t, err)
	})
}
