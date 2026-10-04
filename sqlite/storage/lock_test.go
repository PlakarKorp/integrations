package storage

import (
	"bytes"
	"io"
	"path/filepath"
	"testing"

	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPutLockRenewalUpdatesData(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "repo.db")

	st, err := NewStore(ctx, "sqlite", map[string]string{"location": "sqlite://" + path})
	require.NoError(t, err)
	require.NoError(t, st.Create(ctx, []byte("config")))
	defer st.Close(ctx)

	id := objects.MAC{1}
	for _, data := range []string{"lock-v1", "lock-v2"} {
		_, err := st.Put(ctx, storage.StorageResourceLock, id, bytes.NewReader([]byte(data)))
		require.NoError(t, err)
	}

	rd, err := st.Get(ctx, storage.StorageResourceLock, id, nil)
	require.NoError(t, err)
	defer rd.Close()
	got, err := io.ReadAll(rd)
	require.NoError(t, err)
	assert.Equal(t, "lock-v2", string(got))
}
