// Reproduces a real plakar backup driven by the IMAP importer against a live
// server (Dovecot in Docker). Skipped unless IMAP_E2E_ADDR is set:
//
//	IMAP_E2E_ADDR=localhost:11143 go test ./importer/ -run TestBackup -v
package importer_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"testing"
	"time"

	imapimporter "github.com/PlakarKorp/integrations/imap/importer"

	"github.com/PlakarKorp/kloset/caching"
	"github.com/PlakarKorp/kloset/caching/pebble"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/hashing"
	"github.com/PlakarKorp/kloset/kcontext"
	"github.com/PlakarKorp/kloset/logging"
	"github.com/PlakarKorp/kloset/repository"
	"github.com/PlakarKorp/kloset/resources"
	"github.com/PlakarKorp/kloset/snapshot"
	"github.com/PlakarKorp/kloset/versioning"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/stretchr/testify/require"

	_ "github.com/PlakarKorp/kloset/testing" // registers the mock:// fs store
)

func addr(t *testing.T) string {
	a := os.Getenv("IMAP_E2E_ADDR")
	if a == "" {
		t.Skip("set IMAP_E2E_ADDR to run the IMAP backup test")
	}
	return a
}

func dialLogin(t *testing.T, a, user, pass string) *imapclient.Client {
	t.Helper()
	c, err := imapclient.DialInsecure(a, nil)
	require.NoError(t, err)
	require.NoError(t, c.Login(user, pass).Wait())
	return c
}

func seedTricky(t *testing.T, a, user string) int {
	t.Helper()
	c := dialLogin(t, a, user, "secret")
	defer func() { _ = c.Logout().Wait() }()

	// wipe
	boxes, _ := c.List("", "*", nil).Collect()
	for i := len(boxes) - 1; i >= 0; i-- {
		if boxes[i].Mailbox != "INBOX" {
			_ = c.Delete(boxes[i].Mailbox).Wait()
		}
	}
	if sel, err := c.Select("INBOX", nil).Wait(); err == nil && sel.NumMessages > 0 {
		var seq imap.SeqSet
		seq.AddRange(1, sel.NumMessages)
		_ = c.Store(seq, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close()
		_ = c.Expunge().Close()
	}

	type m struct {
		mbox, subject string
	}
	msgs := []m{
		{"INBOX", "Hello"},
		{"INBOX", ""},          // empty subject
		{"INBOX", "Hello"},     // DUPLICATE subject in same mailbox
		{"INBOX", "Re: Hello"}, // maps via SafeName
		{"Archive", "a"},
		{"Archive", "a"}, // duplicate subject in Archive too
	}
	created := map[string]bool{}
	for _, msg := range msgs {
		if msg.mbox != "INBOX" && !created[msg.mbox] {
			_ = c.Create(msg.mbox, nil).Wait()
			created[msg.mbox] = true
		}
		raw := []byte(fmt.Sprintf("From: a@b.c\r\nSubject: %s\r\n\r\nbody\r\n", msg.subject))
		cmd := c.Append(msg.mbox, int64(len(raw)), &imap.AppendOptions{Time: time.Unix(1700000000, 0)})
		cmd.Write(raw)
		require.NoError(t, cmd.Close())
		_, err := cmd.Wait()
		require.NoError(t, err)
	}
	return len(msgs)
}

// seedLarge wipes the account and appends n messages of approximately
// bodyBytes each into INBOX, so the body-FETCH phase transfers enough data to
// exercise mid-transfer stalls.
func seedLarge(t *testing.T, a, user string, n, bodyBytes int) {
	t.Helper()
	c := dialLogin(t, a, user, "secret")
	defer func() { _ = c.Logout().Wait() }()

	boxes, _ := c.List("", "*", nil).Collect()
	for i := len(boxes) - 1; i >= 0; i-- {
		if boxes[i].Mailbox != "INBOX" {
			_ = c.Delete(boxes[i].Mailbox).Wait()
		}
	}
	if sel, err := c.Select("INBOX", nil).Wait(); err == nil && sel.NumMessages > 0 {
		var seq imap.SeqSet
		seq.AddRange(1, sel.NumMessages)
		_ = c.Store(seq, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close()
		_ = c.Expunge().Close()
	}

	filler := bytes.Repeat([]byte("x"), bodyBytes)
	for i := 0; i < n; i++ {
		raw := []byte(fmt.Sprintf("From: a@b.c\r\nSubject: large-%d\r\n\r\n%s\r\n", i, filler))
		cmd := c.Append("INBOX", int64(len(raw)), &imap.AppendOptions{Time: time.Unix(1700000000, 0)})
		cmd.Write(raw)
		require.NoError(t, cmd.Close())
		_, err := cmd.Wait()
		require.NoError(t, err)
	}
}

func newFsRepo(t *testing.T) *repository.Repository {
	t.Helper()
	ctx := kcontext.NewKContext()
	ctx.Client = "imap-backup-test/1.0.0"
	ctx.MaxConcurrency = 8
	ctx.SetLogger(logging.NewLogger(os.Stdout, os.Stderr))

	cacheDir, err := os.MkdirTemp("", "imapbk_cache")
	require.NoError(t, err)
	repoDir, err := os.MkdirTemp("", "imapbk_repo")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(cacheDir); os.RemoveAll(repoDir) })
	ctx.SetCache(caching.NewManager(pebble.Constructor(cacheDir)))
	ctx.CacheDir = cacheDir

	st, err := storage.New(ctx, map[string]string{"location": "mock://" + repoDir})
	require.NoError(t, err)

	config := storage.NewConfiguration()
	config.Compression = nil
	config.Encryption = nil
	hasher := hashing.GetHasher(hashing.DEFAULT_HASHING_ALGORITHM)
	serialized, _ := config.ToBytes()
	wrappedRd, err := storage.Serialize(hasher, resources.RT_CONFIG, versioning.GetCurrentVersion(resources.RT_CONFIG), bytes.NewReader(serialized))
	require.NoError(t, err)
	wrapped, _ := io.ReadAll(wrappedRd)
	require.NoError(t, st.Create(ctx, wrapped))
	serializedConfig, err := st.Open(ctx)
	require.NoError(t, err)

	repo, err := repository.New(ctx, nil, st, serializedConfig)
	require.NoError(t, err)
	return repo
}

func TestBackupTrickyMailbox(t *testing.T) {
	a := addr(t)
	want := seedTricky(t, a, "srcuser")

	repo := newFsRepo(t)

	imp, err := imapimporter.NewImporter(repo.AppContext(), &connectors.Options{}, "imap", map[string]string{
		"location": "imap://" + a,
		"username": "srcuser",
		"password": "secret",
		"tls":      "no-tls",
	})
	require.NoError(t, err)
	defer imp.Close(context.Background())

	src, err := snapshot.NewSource(repo.AppContext(), 0, imp)
	require.NoError(t, err)

	builder, err := snapshot.Create(repo, repository.DefaultType, "", [32]byte{}, &snapshot.BuilderOptions{})
	require.NoError(t, err)

	// Run the backup in a goroutine so the test can detect a hang instead of
	// blocking until the test timeout.
	done := make(chan error, 1)
	go func() {
		if err := builder.Backup(src); err != nil {
			done <- err
			return
		}
		done <- builder.Commit()
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("backup hung (did not finish in 30s)\n--- goroutines ---\n%s", buf[:n])
	}
	require.NoError(t, builder.Close())
	require.NoError(t, builder.Repository().RebuildState())

	snap, err := snapshot.Load(repo, builder.Header.Identifier)
	require.NoError(t, err)
	defer snap.Close()

	// Count regular files in the snapshot filesystem.
	fs, err := snap.Filesystem()
	require.NoError(t, err)
	got := 0
	for pathname, err := range fs.Pathnames() {
		require.NoError(t, err)
		if e, err := fs.GetEntry(pathname); err == nil && !e.Stat().Mode().IsDir() {
			got++
		}
	}
	t.Logf("seeded %d messages, snapshot has %d files", want, got)
	require.Equal(t, want, got, "snapshot file count does not match seeded message count")
}
