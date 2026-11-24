// Drives the IMAP importer the way a backup engine does — run Import, consume
// the records concurrently and read every message body — against a live server
// (Dovecot in Docker). Skipped unless IMAP_E2E_ADDR is set:
//
//	IMAP_E2E_ADDR=localhost:11143 go test ./importer/ -run TestBackup -v
package importer_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PlakarKorp/integration-sdk"
	imapimporter "github.com/PlakarKorp/integrations/imap/importer"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/stretchr/testify/require"
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
		_, err := cmd.Write(raw)
		require.NoError(t, err)
		require.NoError(t, cmd.Close())
		_, err = cmd.Wait()
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
		_, err := cmd.Write(raw)
		require.NoError(t, err)
		require.NoError(t, cmd.Close())
		_, err = cmd.Wait()
		require.NoError(t, err)
	}
}

// importRun is what a backup engine would have seen from one Import run.
type importRun struct {
	err      error                 // Import's own return value
	recErrs  []*integration.Record // error records emitted by Import
	files    []string              // pathnames of the regular-file records, in arrival order
	bodies   map[string][]byte     // pathname -> body, for bodies read successfully
	readErrs map[string]error      // pathname -> error returned while reading the body
}

// runImport runs imp.Import and consumes its records with `readers` concurrent
// workers, reading every message body in full as a backup engine would. It
// fails the test with a goroutine dump if the run (Import plus all body reads)
// does not finish within timeout.
func runImport(t *testing.T, imp integration.Importer, readers int, timeout time.Duration) *importRun {
	t.Helper()
	ctx := context.Background()
	records := make(chan *integration.Record, 64)
	results := make(chan *integration.Result) // never fed: the importer ignores results
	run := &importRun{bodies: map[string][]byte{}, readErrs: map[string]error{}}

	importErr := make(chan error, 1)
	go func() {
		_, err := imp.Import(ctx, records, results)
		importErr <- err
	}()

	var mu sync.Mutex
	var wg sync.WaitGroup
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rec := range records {
				if rec.Err != nil {
					mu.Lock()
					run.recErrs = append(run.recErrs, rec)
					mu.Unlock()
					continue
				}
				if rec.FileInfo.Lmode.IsDir() {
					continue
				}
				body, err := io.ReadAll(rec.Reader)
				_ = rec.Reader.Close()
				mu.Lock()
				run.files = append(run.files, rec.Pathname)
				if err != nil {
					run.readErrs[rec.Pathname] = err
				} else {
					run.bodies[rec.Pathname] = body
				}
				mu.Unlock()
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		run.err = <-importErr
		close(done)
	}()

	select {
	case <-done:
		close(results)
		return run
	case <-time.After(timeout):
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("import hung (did not finish in %s)\n--- goroutines ---\n%s", timeout, buf[:n])
		return nil
	}
}

// requireCleanRun asserts that Import succeeded, emitted no error record and
// that every message body could be read.
func requireCleanRun(t *testing.T, run *importRun) {
	t.Helper()
	require.NoError(t, run.err)
	for _, r := range run.recErrs {
		t.Errorf("import error record at %q: %v", r.Pathname, r.Err)
	}
	require.Empty(t, run.recErrs)
	require.Empty(t, run.readErrs, "message bodies failed to read")
}

// requireDistinctPaths asserts that no two file records land on the same
// (cleaned) snapshot path, which would collapse messages into one file.
func requireDistinctPaths(t *testing.T, files []string) {
	t.Helper()
	seen := map[string]bool{}
	for _, f := range files {
		require.Equal(t, path.Clean(f), f, "record path %q is not clean", f)
		require.False(t, seen[f], "two messages map to the same path %q", f)
		seen[f] = true
	}
}

func newImporter(t *testing.T, config map[string]string) integration.Importer {
	t.Helper()
	imp, err := imapimporter.NewImporter(context.Background(), &integration.Options{}, "imap", config)
	require.NoError(t, err)
	t.Cleanup(func() { _ = imp.Close(context.Background()) })
	return imp
}

func TestBackupTrickyMailbox(t *testing.T) {
	a := addr(t)
	want := seedTricky(t, a, "srcuser")

	imp := newImporter(t, map[string]string{
		"location": "imap://" + a,
		"username": "srcuser",
		"password": "secret",
		"tls":      "no-tls",
	})

	run := runImport(t, imp, 8, 30*time.Second)
	requireCleanRun(t, run)

	t.Logf("seeded %d messages, import produced %d files", want, len(run.files))
	require.Len(t, run.files, want, "file record count does not match seeded message count")
	requireDistinctPaths(t, run.files)
	for p, body := range run.bodies {
		require.Contains(t, string(body), "Subject:", "body of %q is not the RFC822 message", p)
	}
}

func TestBackupSubfolderScope(t *testing.T) {
	a := addr(t)

	// Seed: INBOX, a sibling Other, and an Archive subtree (Archive + Archive.2024).
	c := dialLogin(t, a, "srcuser", "secret")
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
	put := func(mbox, subj string) {
		if mbox != "INBOX" {
			_ = c.Create(mbox, nil).Wait()
		}
		raw := []byte(fmt.Sprintf("From: a@b.c\r\nSubject: %s\r\n\r\nbody\r\n", subj))
		cmd := c.Append(mbox, int64(len(raw)), &imap.AppendOptions{Time: time.Unix(1700000000, 0)})
		_, err := cmd.Write(raw)
		require.NoError(t, err)
		require.NoError(t, cmd.Close())
		_, err = cmd.Wait()
		require.NoError(t, err)
	}
	put("INBOX", "inbox-msg")
	put("Other", "other-msg")
	put("Archive", "archive-root")
	put("Archive.2024", "archive-nested")
	_ = c.Logout().Wait()

	imp := newImporter(t, map[string]string{
		"location": "imap://" + a + "/Archive", // scope to the Archive subtree
		"username": "srcuser",
		"password": "secret",
		"tls":      "no-tls",
	})

	run := runImport(t, imp, 8, 30*time.Second)
	requireCleanRun(t, run)
	files := run.files
	t.Logf("subfolder-scoped import files: %v", files)

	require.Len(t, files, 2, "expected exactly the two Archive-subtree messages")
	for _, f := range files {
		require.True(t, strings.HasPrefix(f, "/Archive/"), "file %q escaped the Archive scope", f)
	}
	// And nothing from INBOX or the sibling Other leaked in.
	for _, f := range files {
		require.NotContains(t, f, "/Other/")
		require.NotContains(t, f, "/INBOX/")
	}
}
