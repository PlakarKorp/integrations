package storage

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/stretchr/testify/require"
)

var listResources = []struct {
	name string
	res  integration.StorageResource
}{
	{"packfile", integration.StorageResourcePackfile},
	{"state", integration.StorageResourceState},
	{"lock", integration.StorageResourceLock},
}

func newListStore(t *testing.T, res integration.StorageResource, n int) (integration.Store, []integration.MAC) {
	t.Helper()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "repo.db")

	st, err := NewStore(ctx, "sqlite", map[string]string{"location": "sqlite://" + path})
	require.NoError(t, err)
	require.NoError(t, st.Create(ctx, []byte("config")))
	t.Cleanup(func() { _ = st.Close(ctx) })

	macs := make([]integration.MAC, n)
	for i := range macs {
		macs[i] = integration.MAC{byte(i + 1), 0xff}
		_, err := st.Put(ctx, res, 0, macs[i], bytes.NewReader([]byte("data")))
		require.NoError(t, err)
	}
	return st, macs
}

func TestListReturnsEveryEntry(t *testing.T) {
	for _, tt := range listResources {
		t.Run(tt.name, func(t *testing.T) {
			st, macs := newListStore(t, tt.res, 10)

			var got []integration.MAC
			for e, err := range st.List(t.Context(), tt.res, 0) {
				require.NoError(t, err)
				got = append(got, e.MAC)
			}
			require.ElementsMatch(t, macs, got)
		})
	}
}

// List streams rows from an open query, so writing to the store while
// iterating must not fail on a locked database.
func TestListAllowsWritesDuringIteration(t *testing.T) {
	for _, tt := range listResources {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			st, macs := newListStore(t, tt.res, 10)

			seen := 0
			for e, err := range st.List(ctx, tt.res, 0) {
				require.NoError(t, err)
				require.NoError(t, st.Delete(ctx, tt.res, 0, e.MAC))
				_, err = st.Put(ctx, tt.res, 0, integration.MAC{byte(seen + 1), 0xee}, bytes.NewReader([]byte("new")))
				require.NoError(t, err)
				seen++
				if seen == len(macs) {
					break
				}
			}
			require.Equal(t, len(macs), seen)
		})
	}
}

func TestListStopsWhenConsumerBreaks(t *testing.T) {
	for _, tt := range listResources {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			st, macs := newListStore(t, tt.res, 10)

			n := 0
			for _, err := range st.List(ctx, tt.res, 0) {
				require.NoError(t, err)
				n++
				break
			}
			require.Equal(t, 1, n)

			n = 0
			for _, err := range st.List(ctx, tt.res, 0) {
				require.NoError(t, err)
				n++
			}
			require.Equal(t, len(macs), n)
		})
	}
}

// The schema has no timestamp, so MTime stays zero.
func TestListFillsSize(t *testing.T) {
	for _, tt := range listResources {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			st, _ := newListStore(t, tt.res, 0)

			want := map[integration.MAC]int64{
				{0x01}: 0,
				{0x02}: 1,
				{0x03}: 4096,
			}
			for mac, size := range want {
				_, err := st.Put(ctx, tt.res, 0, mac, bytes.NewReader(make([]byte, size)))
				require.NoError(t, err)
			}

			got := map[integration.MAC]int64{}
			for e, err := range st.List(ctx, tt.res, 0) {
				require.NoError(t, err)
				require.True(t, e.MTime.IsZero())
				got[e.MAC] = e.Size
			}
			require.Equal(t, want, got)
		})
	}
}
