package storage

import (
	"bytes"
	"io"
	"path/filepath"
	"testing"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPutLockRenewalUpdatesData(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "repo.db")

	st, err := NewStore(ctx, "sqlite", map[string]string{"location": "sqlite://" + path})
	require.NoError(t, err)
	require.NoError(t, st.Create(ctx, []byte("config")))
	defer func() { _ = st.Close(ctx) }()

	id := integration.MAC{1}
	for _, data := range []string{"lock-v1", "lock-v2"} {
		_, err := st.Put(ctx, integration.StorageResourceLock, 0, id, bytes.NewReader([]byte(data)))
		require.NoError(t, err)
	}

	rd, err := st.Get(ctx, integration.StorageResourceLock, 0, id, nil)
	require.NoError(t, err)
	defer func() { _ = rd.Close() }()
	got, err := io.ReadAll(rd)
	require.NoError(t, err)
	assert.Equal(t, "lock-v2", string(got))
}
