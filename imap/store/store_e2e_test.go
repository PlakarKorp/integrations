// End-to-end tests for the IMAP-backed integration.Store. They are skipped
// unless IMAP_E2E_ADDR is set, e.g.:
//
//	IMAP_E2E_ADDR=localhost:11143 go test ./store/ -run TestStore -v
//
// They drive the Store interface directly: one store instance creates a
// repository and writes config, packfile, state and lock blobs into it, then a
// fresh instance reopens it and reads every blob back — i.e. a backup and a
// restore through an IMAP server, as seen by the storage layer.
package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"os"
	"testing"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/stretchr/testify/require"
)

func storeConfig(t *testing.T) map[string]string {
	t.Helper()
	addr := os.Getenv("IMAP_E2E_ADDR")
	if addr == "" {
		t.Skip("set IMAP_E2E_ADDR to run the IMAP store end-to-end test")
	}
	// Use a different account than the importer tests so the two test packages
	// don't clobber each other's mailboxes when run in parallel (go test ./...).
	return map[string]string{
		"location": "imap://" + addr,
		"username": envOr("IMAP_E2E_STORE_USER", "dstuser"),
		"password": "secret",
		"tls":      "no-tls",
		"root":     "PlakarStore",
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// wipeRoot deletes the store's root mailbox tree so the test is repeatable.
func wipeRoot(t *testing.T, cfg map[string]string) {
	t.Helper()
	addr := cfg["location"][len("imap://"):]
	c, err := imapclient.DialInsecure(addr, nil)
	require.NoError(t, err)
	defer func() { _ = c.Logout().Wait() }()
	require.NoError(t, c.Login(cfg["username"], cfg["password"]).Wait())

	boxes, err := c.List("", cfg["root"]+"*", nil).Collect()
	require.NoError(t, err)
	// delete deepest first
	for i := len(boxes) - 1; i >= 0; i-- {
		_ = c.Delete(boxes[i].Mailbox).Wait()
	}
}

// listMACs drains Store.List for a resource.
func listMACs(t *testing.T, st integration.Store, res integration.StorageResource) []integration.MAC {
	t.Helper()
	var macs []integration.MAC
	for e, err := range st.List(context.Background(), res, 0) {
		require.NoError(t, err)
		macs = append(macs, e.MAC)
	}
	return macs
}

// getBlob reads a blob (or a range of it) back from the store.
func getBlob(t *testing.T, st integration.Store, res integration.StorageResource, mac integration.MAC, rg *integration.Range) []byte {
	t.Helper()
	rc, err := st.Get(context.Background(), res, 0, mac, rg)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	return b
}

func TestStoreBackupRestore(t *testing.T) {
	cfg := storeConfig(t)
	wipeRoot(t, cfg)
	ctx := context.Background()

	// A packfile large enough to be fetched back in several ranged reads, as
	// a restore reads chunks out of a packfile.
	big := make([]byte, 3*1024*1024)
	_, err := rand.Read(big)
	require.NoError(t, err)

	type blob struct {
		res  integration.StorageResource
		mac  integration.MAC
		data []byte
	}
	blobs := []blob{
		{integration.StorageResourcePackfile, integration.MAC{0x01}, []byte("hello world!\n")},
		{integration.StorageResourcePackfile, integration.MAC{0x02}, big},
		{integration.StorageResourceState, integration.MAC{0x03}, []byte("state")},
		{integration.StorageResourceLock, integration.MAC{0x04}, []byte("lock")},
	}
	configBlob := []byte("repository-configuration-blob")

	// Backup: create the repository and write every blob.
	writer, err := NewStore(ctx, "imap", cfg)
	require.NoError(t, err)
	require.NoError(t, writer.Create(ctx, configBlob), "store Create")
	for _, b := range blobs {
		n, err := writer.Put(ctx, b.res, 0, b.mac, bytes.NewReader(b.data))
		require.NoError(t, err)
		require.Equal(t, int64(len(b.data)), n)
	}
	require.NoError(t, writer.Close(ctx))

	// Restore: a fresh store instance (no cached index) reopens the repository.
	reader, err := NewStore(ctx, "imap", cfg)
	require.NoError(t, err)
	defer func() { _ = reader.Close(ctx) }()

	gotConfig, err := reader.Open(ctx)
	require.NoError(t, err, "store Open")
	require.Equal(t, configBlob, gotConfig)

	byRes := map[integration.StorageResource][]integration.MAC{}
	for _, b := range blobs {
		byRes[b.res] = append(byRes[b.res], b.mac)
		require.Equal(t, b.data, getBlob(t, reader, b.res, b.mac, nil))
	}
	for res, macs := range byRes {
		require.ElementsMatch(t, macs, listMACs(t, reader, res), "List(%v)", res)
	}

	// Reassemble the large packfile from 1 MiB ranged reads.
	var reassembled []byte
	const step = 1024 * 1024
	for off := 0; off < len(big); off += step {
		rg := &integration.Range{Offset: uint64(off), Length: step}
		reassembled = append(reassembled, getBlob(t, reader, integration.StorageResourcePackfile, integration.MAC{0x02}, rg)...)
	}
	require.Len(t, reassembled, len(big))
	require.True(t, bytes.Equal(big, reassembled), "large blob mismatch after ranged IMAP round-trip")
}

// TestStoreContract exercises the raw Store interface (Put/Get/List/Delete with
// ranged reads) directly, independent of the repository layer.
func TestStoreContract(t *testing.T) {
	cfg := storeConfig(t)
	cfg["root"] = "PlakarStoreContract"
	wipeRoot(t, cfg)

	ctx := context.Background()
	st, err := NewStore(ctx, "imap", cfg)
	require.NoError(t, err)
	defer func() { _ = st.Close(ctx) }()

	// Create initializes the mailboxes + config.
	cfgBlob := []byte("repository-configuration-blob")
	require.NoError(t, st.Create(ctx, cfgBlob))

	// Open returns the config.
	gotCfg, err := st.Open(ctx)
	require.NoError(t, err)
	require.Equal(t, cfgBlob, gotCfg)

	// Create again must fail (repository already exists).
	require.Error(t, st.Create(ctx, cfgBlob))

	// Put a couple of packfile blobs.
	blobA := bytes.Repeat([]byte("A"), 1000)
	blobB := bytes.Repeat([]byte("B"), 50)
	macA := integration.MAC{0x01, 0x02, 0x03}
	macB := integration.MAC{0xaa, 0xbb, 0xcc}

	n, err := st.Put(ctx, integration.StorageResourcePackfile, 0, macA, bytes.NewReader(blobA))
	require.NoError(t, err)
	require.Equal(t, int64(len(blobA)), n)
	_, err = st.Put(ctx, integration.StorageResourcePackfile, 0, macB, bytes.NewReader(blobB))
	require.NoError(t, err)

	// List returns both MACs.
	require.ElementsMatch(t, []integration.MAC{macA, macB}, listMACs(t, st, integration.StorageResourcePackfile))

	// Get (full) round-trips bytes.
	require.True(t, bytes.Equal(blobA, getBlob(t, st, integration.StorageResourcePackfile, macA, nil)))

	// Get (ranged) returns the requested slice.
	gotRange := getBlob(t, st, integration.StorageResourcePackfile, macA, &integration.Range{Offset: 10, Length: 5})
	require.Equal(t, blobA[10:15], gotRange)

	// Delete removes a blob.
	require.NoError(t, st.Delete(ctx, integration.StorageResourcePackfile, 0, macB))
	require.ElementsMatch(t, []integration.MAC{macA}, listMACs(t, st, integration.StorageResourcePackfile))

	// Getting the deleted blob now fails.
	_, err = st.Get(ctx, integration.StorageResourcePackfile, 0, macB, nil)
	require.Error(t, err)
}
