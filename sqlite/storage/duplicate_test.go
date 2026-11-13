package storage

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/stretchr/testify/require"
)

func TestPutDuplicateFailsForContentAddressedResources(t *testing.T) {
	tests := []struct {
		name string
		res  integration.StorageResource
	}{
		{"packfile", integration.StorageResourcePackfile},
		{"state", integration.StorageResourceState},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "repo.db")

			st, err := NewStore(ctx, "sqlite", map[string]string{"location": "sqlite://" + path})
			require.NoError(t, err)
			require.NoError(t, st.Create(ctx, []byte("config")))
			defer func() { _ = st.Close(ctx) }()

			id := integration.MAC{1}
			_, err = st.Put(ctx, tt.res, 0, id, bytes.NewReader([]byte("v1")))
			require.NoError(t, err)

			_, err = st.Put(ctx, tt.res, 0, id, bytes.NewReader([]byte("v2")))
			require.Error(t, err)
		})
	}
}
