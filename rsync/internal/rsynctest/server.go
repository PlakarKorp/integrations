package rsynctest

import (
	"context"
	"net"
	"testing"

	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/repository"
	"github.com/PlakarKorp/kloset/snapshot"
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

// Backup backs up imp into repo, with parent as the previous snapshot if not
// nil, and returns the snapshot.
func Backup(t *testing.T, repo *repository.Repository, imp importer.Importer, parent *snapshot.Snapshot) *snapshot.Snapshot {
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
