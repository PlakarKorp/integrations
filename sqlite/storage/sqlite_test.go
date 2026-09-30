//go:build !windows

package storage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateRestrictsFileModes(t *testing.T) {
	// A strict umask such as 077 would mask sqlite's default 0644 down to
	// 0600 and hide the bug, so force the common permissive one.
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "repo.db")

	st, err := NewStore(ctx, "sqlite", map[string]string{"location": "sqlite://" + path})
	require.NoError(t, err)
	require.NoError(t, st.Create(ctx, []byte("config")))
	defer st.Close(ctx)

	_, err = st.Put(ctx, storage.StorageResourceState, objects.MAC{1}, bytes.NewReader([]byte("state")))
	require.NoError(t, err)

	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		fi, err := os.Stat(name)
		require.NoError(t, err)
		assert.Zero(t, fi.Mode().Perm()&0o077, "%s has mode %#o", filepath.Base(name), fi.Mode().Perm())
	}
}
