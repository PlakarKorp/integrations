package storage

import (
	"path/filepath"
	"testing"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/stretchr/testify/require"
)

func TestGetMissingPackfileRangeReturnsErrBlobNotFound(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "repo.db")

	st, err := NewStore(ctx, "sqlite", map[string]string{"location": "sqlite://" + path})
	require.NoError(t, err)
	require.NoError(t, st.Create(ctx, []byte("config")))
	defer func() { _ = st.Close(ctx) }()

	_, err = st.Get(ctx, integration.StorageResourcePackfile, 0, integration.MAC{1}, &integration.Range{Offset: 0, Length: 4})
	require.ErrorIs(t, err, ErrBlobNotFound)
}
