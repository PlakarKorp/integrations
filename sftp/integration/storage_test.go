package integration

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sync"
	"testing"

	sdk "github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integrations/sftp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newStore(t *testing.T, s *sshServer, root string) sdk.Store {
	t.Helper()
	st, err := sftp.NewStore(t.Context(), "sftp", s.config(root))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close(t.Context()) })
	return st
}

func macOf(b ...byte) sdk.MAC {
	var m sdk.MAC
	copy(m[:], b)
	return m
}

func list(t *testing.T, st sdk.Store, res sdk.StorageResource) []sdk.MAC {
	t.Helper()
	var macs []sdk.MAC
	for e, err := range st.List(t.Context(), res, 0) {
		require.NoError(t, err)
		macs = append(macs, e.MAC)
	}
	return macs
}

func get(t *testing.T, st sdk.Store, res sdk.StorageResource, m sdk.MAC, rg *sdk.Range) string {
	t.Helper()
	rd, err := st.Get(t.Context(), res, 0, m, rg)
	require.NoError(t, err)
	defer func() { _ = rd.Close() }()
	b, err := io.ReadAll(rd)
	require.NoError(t, err)
	return string(b)
}

func TestStoreRoundTrip(t *testing.T) {
	s := getServer(t)
	dir := testDir(t)
	ctx := t.Context()

	st := newStore(t, s, dir)
	require.NoError(t, st.Create(ctx, []byte("config")))
	assert.Equal(t, "700", s.stat(t, dir).mode, "repository is private")
	assert.Equal(t, "700", s.stat(t, dir+"/packfiles/ab").mode, "buckets are private")

	resources := []sdk.StorageResource{
		sdk.StorageResourcePackfile,
		sdk.StorageResourceState,
		sdk.StorageResourceLock,
	}
	for i, res := range resources {
		m := macOf(0xab, byte(i))
		content := fmt.Sprintf("%s content", res)
		n, err := st.Put(ctx, res, 0, m, bytes.NewReader([]byte(content)))
		require.NoError(t, err, "%s", res)
		assert.Equal(t, int64(len(content)), n, "%s", res)
	}

	// A second connection sees what the first one wrote.
	reopened := newStore(t, s, dir)
	config, err := reopened.Open(ctx)
	require.NoError(t, err)
	assert.Equal(t, "config", string(config))

	for i, res := range resources {
		m := macOf(0xab, byte(i))
		assert.Equal(t, []sdk.MAC{m}, list(t, reopened, res), "%s", res)
		assert.Equal(t, fmt.Sprintf("%s content", res), get(t, reopened, res, m, nil), "%s", res)
	}

	pack := macOf(0xab, 0)
	packContent := fmt.Sprintf("%s content", sdk.StorageResourcePackfile)
	assert.Equal(t, packContent[2:5], get(t, reopened, sdk.StorageResourcePackfile, pack, &sdk.Range{Offset: 2, Length: 3}))
	for i, res := range resources[1:] {
		_, err := reopened.Get(ctx, res, 0, macOf(0xab, byte(i+1)), &sdk.Range{Offset: 0, Length: 1})
		require.ErrorIs(t, err, errors.ErrUnsupported, "%s does not support ranges", res)
	}

	for i, res := range resources {
		m := macOf(0xab, byte(i))
		require.NoError(t, reopened.Delete(ctx, res, 0, m), "%s", res)
		assert.Empty(t, list(t, reopened, res), "%s", res)
		_, err := reopened.Get(ctx, res, 0, m, nil)
		require.ErrorIs(t, err, fs.ErrNotExist, "%s", res)
	}
}

func TestStoreCreateInExistingDirectory(t *testing.T) {
	s := getServer(t)

	empty := testDir(t)
	s.sh(t, `mkdir -p "$1"`, empty)
	require.NoError(t, newStore(t, s, empty).Create(t.Context(), []byte("config")))

	nonEmpty := testDir(t)
	s.sh(t, `mkdir -p "$1" && touch "$1/precious"`, nonEmpty)
	require.Error(t, newStore(t, s, nonEmpty).Create(t.Context(), []byte("config")))
	assert.Equal(t, "precious\n", s.sh(t, `ls "$1"`, nonEmpty), "a refused Create leaves the directory alone")
}

func TestStoreOpenMissing(t *testing.T) {
	s := getServer(t)
	_, err := newStore(t, s, testDir(t)).Open(t.Context())
	require.ErrorIs(t, err, fs.ErrNotExist)
}

// kloset writes packfiles and states from many goroutines over one Store.
func TestStoreConcurrentPuts(t *testing.T) {
	s := getServer(t)
	st := newStore(t, s, testDir(t))
	require.NoError(t, st.Create(t.Context(), []byte("config")))

	const n = 64
	resources := []sdk.StorageResource{sdk.StorageResourcePackfile, sdk.StorageResourceState}
	want := map[sdk.StorageResource][]sdk.MAC{}

	var wg sync.WaitGroup
	for _, res := range resources {
		for i := range n {
			m := macOf(byte(i), byte(res))
			want[res] = append(want[res], m)
			wg.Go(func() {
				_, err := st.Put(t.Context(), res, 0, m, bytes.NewReader(m[:]))
				assert.NoError(t, err, "%s %x", res, m)
			})
		}
	}
	wg.Wait()

	for _, res := range resources {
		require.ElementsMatch(t, want[res], list(t, st, res), "%s", res)
		for _, m := range want[res] {
			assert.Equal(t, string(m[:]), get(t, st, res, m, nil), "%s %x", res, m)
		}
	}
}
