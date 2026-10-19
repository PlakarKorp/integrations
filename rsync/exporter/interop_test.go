package exporter_test

import (
	"context"
	"io"
	"testing"

	"github.com/PlakarKorp/integrations/rsync/internal/rsync"
	"github.com/PlakarKorp/integrations/rsync/internal/rsynctest"
	"github.com/PlakarKorp/kloset/snapshot"
	"github.com/stretchr/testify/require"
)

// TestInterop restores a backup of a real rsync daemon's module into another
// of its modules, and compares both.
func TestInterop(t *testing.T) {
	ctx := context.Background()
	addr := rsynctest.StartRsyncd(t)

	snap := backup(t, "rsync://"+addr+"/pub")
	exp := newExporter(t, "rsync://"+addr+"/restore/round/trip")
	require.NoError(t, snap.Export(exp, "/pub", &snapshot.ExportOptions{}))

	list := func(c *rsync.Client, dir string) map[string]rsync.File {
		files, errs, err := c.List(ctx, dir)
		require.NoError(t, err)
		require.Empty(t, errs)
		byName := make(map[string]rsync.File)
		for _, f := range files {
			// A chrooted daemon cannot resolve names: compare ids.
			f.User, f.Group = "", ""
			byName[f.Name] = f
		}
		return byName
	}
	src := rsync.NewClient(addr, "pub", "", "", 2)
	dst := rsync.NewClient(addr, "restore", "", "", 2)
	want, got := list(src, ""), list(dst, "round/trip")
	require.Equal(t, want, got)

	read := func(c *rsync.Client, name string) []byte {
		rd, err := c.Open(ctx, name)
		require.NoError(t, err, name)
		data, err := io.ReadAll(rd)
		require.NoError(t, err, name)
		require.NoError(t, rd.Close())
		return data
	}
	for name, f := range want {
		if !f.Mode.IsRegular() {
			continue
		}
		require.Equal(t, read(src, name), read(dst, "round/trip/"+name), name)
	}
}
