// Package e2e contains an end-to-end backup+restore test that runs against a
// real IMAP server (e.g. Dovecot in Docker). It is skipped unless IMAP_E2E_ADDR
// is set, e.g.:
//
//	IMAP_E2E_ADDR=localhost:11143 go test ./e2e/ -run TestRoundTrip -v
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/integrations/imap/common"
	"github.com/PlakarKorp/integrations/imap/exporter"
	"github.com/PlakarKorp/integrations/imap/importer"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// seedMessage describes a message to plant on the source server.
type seedMessage struct {
	mailbox string
	subject string
	flags   []imap.Flag
}

// loginClient dials and logs in, retrying a few times to absorb the transient
// EOF Dovecot occasionally returns under rapid connection churn.
func loginClient(t *testing.T, addr, user, pass string) *imapclient.Client {
	t.Helper()
	var lastErr error
	for i := 0; i < 5; i++ {
		c, err := imapclient.DialInsecure(addr, nil)
		if err != nil {
			lastErr = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if err := c.Login(user, pass).Wait(); err != nil {
			_ = c.Close()
			lastErr = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		return c
	}
	t.Fatalf("login %s: %v", user, lastErr)
	return nil
}

func makeRFC822(subject string) []byte {
	body := fmt.Sprintf("From: sender@example.com\r\n"+
		"To: testuser@example.com\r\n"+
		"Subject: %s\r\n"+
		"Date: Mon, 02 Jan 2006 15:04:05 -0700\r\n"+
		"Message-ID: <%s@example.com>\r\n"+
		"\r\n"+
		"Body of message %q.\r\n", subject, common.SafeName(subject), subject)
	return []byte(body)
}

// wipe deletes every mailbox (except INBOX) and empties INBOX for a user, so
// the test is repeatable.
func wipe(t *testing.T, addr, user, pass string) {
	t.Helper()
	c := loginClient(t, addr, user, pass)
	defer func() { _ = c.Logout().Wait() }()

	boxes, err := c.List("", "*", nil).Collect()
	if err != nil {
		t.Fatalf("wipe list: %v", err)
	}
	// Delete deepest mailboxes first.
	names := make([]string, 0, len(boxes))
	for _, b := range boxes {
		if strings.EqualFold(b.Mailbox, "INBOX") {
			continue
		}
		names = append(names, b.Mailbox)
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	for _, n := range names {
		_ = c.Delete(n).Wait()
	}
	// Empty INBOX.
	if sel, err := c.Select("INBOX", nil).Wait(); err == nil && sel.NumMessages > 0 {
		var seq imap.SeqSet
		seq.AddRange(1, sel.NumMessages)
		store := &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}}
		if err := c.Store(seq, store, nil).Close(); err != nil {
			t.Fatalf("wipe store: %v", err)
		}
		_ = c.Expunge().Close()
	}
}

func seed(t *testing.T, addr, user, pass string, msgs []seedMessage) {
	t.Helper()
	c := loginClient(t, addr, user, pass)
	defer func() { _ = c.Logout().Wait() }()

	created := map[string]bool{}
	for _, m := range msgs {
		if m.mailbox != "INBOX" && !created[m.mailbox] {
			if err := c.Create(m.mailbox, nil).Wait(); err != nil && !common.IsAlreadyExists(err) {
				t.Fatalf("seed create %q: %v", m.mailbox, err)
			}
			created[m.mailbox] = true
		}
		raw := makeRFC822(m.subject)
		opts := &imap.AppendOptions{Flags: m.flags, Time: time.Unix(1700000000, 0)}
		cmd := c.Append(m.mailbox, int64(len(raw)), opts)
		if _, err := cmd.Write(raw); err != nil {
			t.Fatalf("seed write %q: %v", m.subject, err)
		}
		if err := cmd.Close(); err != nil {
			t.Fatalf("seed close %q: %v", m.subject, err)
		}
		if _, err := cmd.Wait(); err != nil {
			t.Fatalf("seed append %q: %v", m.subject, err)
		}
	}
}

// snapshotState lists every mailbox and message (subject->flags) for a user, so
// source and destination can be compared structurally.
type mailboxState struct {
	messages map[string][]string // subject -> sorted flag strings (system flags only)
}

func capture(t *testing.T, addr, user, pass string) map[string]*mailboxState {
	t.Helper()
	c := loginClient(t, addr, user, pass)
	defer func() { _ = c.Logout().Wait() }()

	out := map[string]*mailboxState{}
	boxes, err := c.List("", "*", nil).Collect()
	if err != nil {
		t.Fatalf("capture list: %v", err)
	}
	for _, b := range boxes {
		if hasMboxAttr(b.Attrs, imap.MailboxAttrNoSelect) {
			continue
		}
		st := &mailboxState{messages: map[string][]string{}}
		out[b.Mailbox] = st

		sel, err := c.Select(b.Mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			t.Fatalf("capture select %q: %v", b.Mailbox, err)
		}
		if sel.NumMessages == 0 {
			continue
		}
		var seq imap.SeqSet
		seq.AddRange(1, sel.NumMessages)
		fetched, err := c.Fetch(seq, &imap.FetchOptions{Envelope: true, Flags: true}).Collect()
		if err != nil {
			t.Fatalf("capture fetch %q: %v", b.Mailbox, err)
		}
		for _, m := range fetched {
			subject := ""
			if m.Envelope != nil {
				subject = m.Envelope.Subject
			}
			st.messages[subject] = systemFlagStrings(m.Flags)
		}
	}
	return out
}

func systemFlagStrings(flags []imap.Flag) []string {
	keep := map[imap.Flag]bool{
		imap.FlagSeen:     true,
		imap.FlagAnswered: true,
		imap.FlagFlagged:  true,
		imap.FlagDraft:    true,
		imap.FlagDeleted:  true,
	}
	var out []string
	for _, f := range flags {
		if keep[f] {
			out = append(out, string(f))
		}
	}
	sort.Strings(out)
	return out
}

func hasMboxAttr(attrs []imap.MailboxAttr, want imap.MailboxAttr) bool {
	for _, a := range attrs {
		if a == want {
			return true
		}
	}
	return false
}

func TestRoundTrip(t *testing.T) {
	addr := os.Getenv("IMAP_E2E_ADDR")
	if addr == "" {
		t.Skip("set IMAP_E2E_ADDR to run the IMAP end-to-end test")
	}
	const pass = "secret"
	srcUser := envOr("IMAP_E2E_SRC_USER", "srcuser")
	dstUser := envOr("IMAP_E2E_DST_USER", "dstuser")

	// A representative mailbox set: INBOX, a nested hierarchy, a folder name
	// with a space, and every system flag plus a keyword.
	msgs := []seedMessage{
		{"INBOX", "Welcome", []imap.Flag{imap.FlagSeen}},
		{"INBOX", "Unread plain", nil},
		{"INBOX", "Flagged and answered", []imap.Flag{imap.FlagFlagged, imap.FlagAnswered}},
		{"Archive", "Old mail", []imap.Flag{imap.FlagSeen}},
		{"Archive.2024", "Nested deep", []imap.Flag{imap.FlagSeen, imap.FlagFlagged}},
		{"Archive.2024.Q1", "Deeper still", nil},
		{"Drafts", "A draft", []imap.Flag{imap.FlagDraft}},
		{"Work Stuff", "Spaced folder", []imap.Flag{imap.FlagSeen}},
	}

	wipe(t, addr, srcUser, pass)
	wipe(t, addr, dstUser, pass)
	seed(t, addr, srcUser, pass, msgs)

	srcBefore := capture(t, addr, srcUser, pass)
	t.Logf("source has %d mailboxes", len(srcBefore))

	// 1) IMPORT from the source account.
	ctx := context.Background()
	imp, err := importer.NewImporter(ctx, &connectors.Options{}, "imap", map[string]string{
		"location": "imap://" + addr,
		"username": srcUser,
		"password": pass,
		"tls":      "no-tls",
	})
	if err != nil {
		t.Fatalf("NewImporter: %v", err)
	}
	defer imp.Close(ctx)

	type captured struct {
		rec  *connectors.Record
		body []byte
	}
	records := make(chan *connectors.Record, 64)
	results := make(chan *connectors.Result, 64)
	go func() {
		for range results {
		}
	}()

	var imported []captured
	done := make(chan error, 1)
	go func() { done <- imp.Import(ctx, records, results) }()

	for rec := range records {
		if rec.Err != nil {
			t.Errorf("import record error at %q: %v", rec.Pathname, rec.Err)
			continue
		}
		var body []byte
		if !rec.FileInfo.Lmode.IsDir() && rec.Reader != nil {
			b, err := io.ReadAll(rec.Reader)
			rec.Reader.Close()
			if err != nil {
				t.Fatalf("read import body %q: %v", rec.Pathname, err)
			}
			body = b
		}
		imported = append(imported, captured{rec: rec, body: body})
	}
	if err := <-done; err != nil {
		t.Fatalf("Import: %v", err)
	}
	close(results)
	t.Logf("imported %d records", len(imported))

	// Sanity: every seeded message must show up as a record with flags encoded
	// into the file name.
	wantMsgs := 0
	for range msgs {
		wantMsgs++
	}
	fileRecords := 0
	for _, c := range imported {
		if !c.rec.FileInfo.Lmode.IsDir() {
			fileRecords++
		}
	}
	if fileRecords != wantMsgs {
		t.Errorf("imported %d message records, want %d", fileRecords, wantMsgs)
	}

	// 2) EXPORT into the destination account.
	exp, err := exporter.NewExporter(ctx, &connectors.Options{}, "imap", map[string]string{
		"location": "imap://" + addr,
		"username": dstUser,
		"password": pass,
		"tls":      "no-tls",
	})
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	defer exp.Close(ctx)

	expRecords := make(chan *connectors.Record, 64)
	expResults := make(chan *connectors.Result, 64)

	expDone := make(chan error, 1)
	go func() { expDone <- exp.Export(ctx, expRecords, expResults) }()

	go func() {
		// Feed directories first (sorted by depth) then files, mirroring how a
		// real restore streams records.
		dirs := make([]captured, 0)
		files := make([]captured, 0)
		for _, c := range imported {
			if c.rec.FileInfo.Lmode.IsDir() {
				dirs = append(dirs, c)
			} else {
				files = append(files, c)
			}
		}
		sort.SliceStable(dirs, func(i, j int) bool {
			return strings.Count(dirs[i].rec.Pathname, "/") < strings.Count(dirs[j].rec.Pathname, "/")
		})
		emit := func(c captured) {
			r := &connectors.Record{
				Pathname: c.rec.Pathname,
				FileInfo: c.rec.FileInfo,
			}
			if !c.rec.FileInfo.Lmode.IsDir() {
				r.Reader = io.NopCloser(bytes.NewReader(c.body))
			}
			expRecords <- r
		}
		for _, c := range dirs {
			emit(c)
		}
		for _, c := range files {
			emit(c)
		}
		close(expRecords)
	}()

	var exportErrors int
	for res := range expResults {
		if res.Err != nil {
			exportErrors++
			t.Errorf("export error at %q: %v", res.Record.Pathname, res.Err)
		}
	}
	if err := <-expDone; err != nil {
		t.Fatalf("Export: %v", err)
	}
	if exportErrors > 0 {
		t.Fatalf("%d export errors", exportErrors)
	}

	// 3) COMPARE destination against source.
	dstAfter := capture(t, addr, dstUser, pass)

	compareStates(t, srcBefore, dstAfter)
}

func compareStates(t *testing.T, src, dst map[string]*mailboxState) {
	t.Helper()

	// Every source mailbox must exist in the destination with identical
	// messages and flags. (Dovecot auto-creates INBOX, so dst may also have it.)
	for mbox, sstate := range src {
		dstate, ok := dst[mbox]
		if !ok {
			t.Errorf("destination missing mailbox %q", mbox)
			continue
		}
		if len(sstate.messages) != len(dstate.messages) {
			t.Errorf("mailbox %q: src has %d messages, dst has %d",
				mbox, len(sstate.messages), len(dstate.messages))
		}
		for subj, sflags := range sstate.messages {
			dflags, ok := dstate.messages[subj]
			if !ok {
				t.Errorf("mailbox %q: dst missing message %q", mbox, subj)
				continue
			}
			if strings.Join(sflags, ",") != strings.Join(dflags, ",") {
				t.Errorf("mailbox %q message %q: src flags %v != dst flags %v",
					mbox, subj, sflags, dflags)
			}
		}
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
