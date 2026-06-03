// End-to-end test for the IMAP-backed kloset storage.Store. It is skipped
// unless IMAP_E2E_ADDR is set, e.g.:
//
//	IMAP_E2E_ADDR=localhost:11143 go test ./store/ -run TestStore -v
//
// The test drives the full kloset stack (configuration, chunking, packfiles,
// states) on top of the IMAP store: it creates a repository backed by IMAP,
// backs up a set of files into it, then reads them back and verifies the bytes
// — i.e. a real backup-and-restore through an IMAP server.
package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"os"
	"testing"

	"github.com/PlakarKorp/kloset/caching"
	"github.com/PlakarKorp/kloset/caching/pebble"
	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/hashing"
	"github.com/PlakarKorp/kloset/kcontext"
	"github.com/PlakarKorp/kloset/logging"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/repository"
	"github.com/PlakarKorp/kloset/resources"
	ptesting "github.com/PlakarKorp/kloset/testing"
	"github.com/PlakarKorp/kloset/versioning"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/stretchr/testify/require"
)

func storeConfig(t *testing.T) map[string]string {
	t.Helper()
	addr := os.Getenv("IMAP_E2E_ADDR")
	if addr == "" {
		t.Skip("set IMAP_E2E_ADDR to run the IMAP store end-to-end test")
	}
	return map[string]string{
		"location": "imap://" + addr,
		"username": envOr("IMAP_E2E_STORE_USER", "srcuser"),
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

// newIMAPRepository mirrors kloset's testing.GenerateRepository but uses the
// IMAP store as the backend instead of the in-memory mock.
func newIMAPRepository(t *testing.T, cfg map[string]string) *repository.Repository {
	t.Helper()

	ctx := kcontext.NewKContext()
	ctx.Client = "plakar-imap-store-test/1.0.0"
	ctx.MaxConcurrency = 1
	ctx.SetLogger(logging.NewLogger(os.Stdout, os.Stderr))

	tmpCacheDir, err := os.MkdirTemp("", "imap_store_cache")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpCacheDir) })
	ctx.SetCache(caching.NewManager(pebble.Constructor(tmpCacheDir)))
	ctx.CacheDir = tmpCacheDir

	st, err := storage.New(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, st)

	config := storage.NewConfiguration()
	config.Compression = nil
	config.Encryption = nil
	hasher := hashing.GetHasher(hashing.DEFAULT_HASHING_ALGORITHM)

	serialized, err := config.ToBytes()
	require.NoError(t, err)
	wrappedRd, err := storage.Serialize(hasher, resources.RT_CONFIG, versioning.GetCurrentVersion(resources.RT_CONFIG), bytes.NewReader(serialized))
	require.NoError(t, err)
	wrapped, err := io.ReadAll(wrappedRd)
	require.NoError(t, err)

	require.NoError(t, st.Create(ctx, wrapped), "store Create")

	serializedConfig, err := st.Open(ctx)
	require.NoError(t, err, "store Open")

	repo, err := repository.New(ctx, nil, st, serializedConfig)
	require.NoError(t, err, "repository.New")
	return repo
}

func TestStoreBackupRestore(t *testing.T) {
	cfg := storeConfig(t)
	wipeRoot(t, cfg)

	repo := newIMAPRepository(t, cfg)

	// A blob large enough to span multiple chunks/packfiles exercises ranged
	// reads on Get.
	big := make([]byte, 3*1024*1024)
	_, err := rand.Read(big)
	require.NoError(t, err)

	files := []ptesting.MockFile{
		ptesting.NewMockFile("hello.txt", 0o644, "hello world!\n"),
		ptesting.NewMockFile("nested/data.bin", 0o644, string(big)),
		ptesting.NewMockDir("emptydir"),
	}

	snap := ptesting.GenerateSnapshot(t, repo, files)
	defer snap.Close()

	fs, err := snap.Filesystem()
	require.NoError(t, err)

	// hello.txt
	fp, err := fs.Open("hello.txt")
	require.NoError(t, err)
	got, err := io.ReadAll(fp)
	require.NoError(t, err)
	fp.Close()
	require.Equal(t, "hello world!\n", string(got))

	// the large binary file, verifying multi-chunk reassembly through IMAP
	fp, err = fs.Open("nested/data.bin")
	require.NoError(t, err)
	gotBig, err := io.ReadAll(fp)
	require.NoError(t, err)
	fp.Close()
	require.Equal(t, len(big), len(gotBig))
	require.True(t, bytes.Equal(big, gotBig), "large file content mismatch after IMAP round-trip")

	t.Logf("backed up and restored %d files through the IMAP store", len(files))
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
	defer st.Close(ctx)

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
	macA := objects.MAC{0x01, 0x02, 0x03}
	macB := objects.MAC{0xaa, 0xbb, 0xcc}

	n, err := st.Put(ctx, storage.StorageResourcePackfile, macA, bytes.NewReader(blobA))
	require.NoError(t, err)
	require.Equal(t, int64(len(blobA)), n)
	_, err = st.Put(ctx, storage.StorageResourcePackfile, macB, bytes.NewReader(blobB))
	require.NoError(t, err)

	// List returns both MACs.
	macs, err := st.List(ctx, storage.StorageResourcePackfile)
	require.NoError(t, err)
	require.ElementsMatch(t, []objects.MAC{macA, macB}, macs)

	// Get (full) round-trips bytes.
	rc, err := st.Get(ctx, storage.StorageResourcePackfile, macA, nil)
	require.NoError(t, err)
	gotA, _ := io.ReadAll(rc)
	rc.Close()
	require.True(t, bytes.Equal(blobA, gotA))

	// Get (ranged) returns the requested slice.
	rc, err = st.Get(ctx, storage.StorageResourcePackfile, macA, &storage.Range{Offset: 10, Length: 5})
	require.NoError(t, err)
	gotRange, _ := io.ReadAll(rc)
	rc.Close()
	require.Equal(t, blobA[10:15], gotRange)

	// Delete removes a blob.
	require.NoError(t, st.Delete(ctx, storage.StorageResourcePackfile, macB))
	macs, err = st.List(ctx, storage.StorageResourcePackfile)
	require.NoError(t, err)
	require.ElementsMatch(t, []objects.MAC{macA}, macs)

	// Getting the deleted blob now fails.
	_, err = st.Get(ctx, storage.StorageResourcePackfile, macB, nil)
	require.Error(t, err)
}
