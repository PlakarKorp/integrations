package storage

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := NewStore(t.Context(), "fs", map[string]string{"location": filepath.Join(t.TempDir(), "repo")})
	require.NoError(t, err)
	s := st.(*Store)
	require.NoError(t, s.Create(t.Context(), []byte("config")))
	return s
}

func mac(b byte) integration.MAC {
	var m integration.MAC
	m[0] = b
	m[31] = b
	return m
}

func list(t *testing.T, s *Store, res integration.StorageResource) ([]integration.MAC, error) {
	t.Helper()
	var ret []integration.MAC
	for entry, err := range s.List(t.Context(), res, 0) {
		if err != nil {
			return ret, err
		}
		ret = append(ret, entry.MAC)
	}
	return ret, nil
}

func TestStorageList(t *testing.T) {
	for _, res := range []integration.StorageResource{
		integration.StorageResourcePackfile,
		integration.StorageResourceState,
		integration.StorageResourceLock,
	} {
		t.Run(res.String(), func(t *testing.T) {
			s := newTestStore(t)

			got, err := list(t, s, res)
			require.NoError(t, err)
			require.Empty(t, got)

			want := []integration.MAC{mac(0x00), mac(0x01), mac(0x7f), mac(0xff)}
			for _, m := range want {
				_, err := s.Put(t.Context(), res, 0, m, bytes.NewReader([]byte("data")))
				require.NoError(t, err)
			}

			got, err = list(t, s, res)
			require.NoError(t, err)
			require.ElementsMatch(t, want, got)
		})
	}
}

func TestStorageList_FillsSizeAndMTime(t *testing.T) {
	mtime := time.Date(2026, 11, 25, 13, 24, 35, 0, time.UTC)
	for _, tt := range []struct {
		res  integration.StorageResource
		path func(s *Store, m integration.MAC) string
	}{
		{integration.StorageResourcePackfile, func(s *Store, m integration.MAC) string {
			return s.Path("packfiles", fmt.Sprintf("%02x", m[0]), fmt.Sprintf("%064x", m))
		}},
		{integration.StorageResourceState, func(s *Store, m integration.MAC) string {
			return s.Path("states", fmt.Sprintf("%02x", m[0]), fmt.Sprintf("%064x", m))
		}},
		{integration.StorageResourceLock, func(s *Store, m integration.MAC) string {
			return s.Path("locks", fmt.Sprintf("%064x", m))
		}},
	} {
		t.Run(tt.res.String(), func(t *testing.T) {
			s := newTestStore(t)
			m := mac(0x42)
			_, err := s.Put(t.Context(), tt.res, 0, m, bytes.NewReader([]byte("some data")))
			require.NoError(t, err)
			require.NoError(t, os.Chtimes(tt.path(s, m), mtime, mtime))

			var got []integration.StorageResourceEntry
			for entry, err := range s.List(t.Context(), tt.res, 0) {
				require.NoError(t, err)
				got = append(got, *entry)
			}
			require.Len(t, got, 1)
			require.Equal(t, m, got[0].MAC)
			require.Equal(t, int64(len("some data")), got[0].Size)
			require.True(t, mtime.Equal(got[0].MTime), "mtime %v, want %v", got[0].MTime, mtime)
		})
	}
}

func TestStorageList_SkipsBadNames(t *testing.T) {
	s := newTestStore(t)
	m := mac(0x12)
	_, err := s.Put(t.Context(), integration.StorageResourcePackfile, 0, m, bytes.NewReader([]byte("data")))
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(s.Path("packfiles", "12", "not-hex"), []byte("x"), 0600))
	require.NoError(t, os.WriteFile(s.Path("packfiles", "12", "deadbeef"), []byte("x"), 0600))

	got, err := list(t, s, integration.StorageResourcePackfile)
	require.NoError(t, err)
	require.Equal(t, []integration.MAC{m}, got)
}

func TestStorageList_StopsEarly(t *testing.T) {
	s := newTestStore(t)
	for i := range 16 {
		_, err := s.Put(t.Context(), integration.StorageResourcePackfile, 0, mac(byte(i)), bytes.NewReader([]byte("data")))
		require.NoError(t, err)
	}

	n := 0
	for _, err := range s.List(t.Context(), integration.StorageResourcePackfile, 0) {
		require.NoError(t, err)
		n++
		break
	}
	require.Equal(t, 1, n)
}

func TestStorageList_ReadError(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, os.RemoveAll(s.Path("states")))

	_, err := list(t, s, integration.StorageResourceState)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestStorageList_UnsupportedResource(t *testing.T) {
	s := newTestStore(t)
	_, err := list(t, s, integration.StorageResourceUndefined)
	require.Error(t, err)
}
