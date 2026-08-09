package importer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/PlakarKorp/integrations/imap/common"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/emersion/go-imap/v2"
)

func init() {
	importer.Register("imap", 0, NewImporter)
}

type Importer struct {
	connector common.ImapConnector
	pool      *common.ConnectionPool
}

func NewImporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	imp := &Importer{}
	if err := imp.connector.InitFromConfig(config); err != nil {
		return nil, err
	}
	return imp, nil
}

func (imp *Importer) Root() string          { return "/" }
func (imp *Importer) Origin() string        { return imp.connector.Address }
func (imp *Importer) Type() string          { return "imap" }
func (imp *Importer) Flags() location.Flags { return 0 }

func (imp *Importer) Ping(ctx context.Context) error {
	s, err := imp.connector.Connect()
	if err != nil {
		return err
	}
	defer func() { _ = s.Logout() }()

	_, err = s.Client.List("", "*", nil).Collect()
	return err
}

func (imp *Importer) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	session, err := imp.connector.Connect()
	if err != nil {
		return err
	}
	defer func() { _ = session.Logout() }()

	// The per-message body readers are consumed lazily by the backup engine,
	// possibly after Import returns. The pool therefore outlives Import and is
	// torn down in Close().
	pool, err := common.NewPool(imp.connector, 5)
	if err != nil {
		return err
	}
	imp.pool = pool

	all, err := session.Client.List("", "*", nil).Collect()
	if err != nil {
		return fmt.Errorf("LIST failed: %w", err)
	}

	// If the location URL carried a path, scope the backup to that mailbox and
	// its descendants. The native prefix name uses the server delimiter (which
	// the LIST results report); a message-less holder mailbox is fine.
	mboxes, err := imp.scopeMailboxes(all)
	if err != nil {
		return err
	}

	// root dir
	records <- connectors.NewRecord("/", "", objects.FileInfo{
		Lname:    "/",
		Lmode:    os.ModeDir | 0o700,
		Lsize:    0,
		LmodTime: time.Unix(0, 0),
	}, nil, nil)

	// Emit a directory record for every mailbox and its ancestors. The kloset
	// path is derived from the mailbox name using the server delimiter so the
	// hierarchy round-trips regardless of the delimiter character.
	emitted := map[string]struct{}{"/": {}}
	emitDir := func(p string) {
		p = path.Clean(p)
		if _, ok := emitted[p]; ok {
			return
		}
		emitted[p] = struct{}{}
		records <- connectors.NewRecord(p, "", objects.FileInfo{
			Lname:    path.Base(p),
			Lmode:    os.ModeDir | 0o700,
			Lsize:    0,
			LmodTime: time.Unix(0, 0),
		}, nil, nil)
	}

	for _, m := range mboxes {
		// Skip \NonExistent / \NoSelect intermediate markers that have no path.
		if m.Mailbox == "" {
			continue
		}
		mboxPath := common.MailboxToPath(m.Mailbox, m.Delim)
		curr := "/"
		for _, part := range strings.Split(strings.TrimPrefix(mboxPath, "/"), "/") {
			if part == "" {
				continue
			}
			curr = path.Join(curr, part)
			emitDir(curr)
		}
	}

	for _, m := range mboxes {
		if m.Mailbox == "" || hasAttr(m.Attrs, imap.MailboxAttrNonExistent) || hasAttr(m.Attrs, imap.MailboxAttrNoSelect) {
			continue
		}
		mboxPath := common.MailboxToPath(m.Mailbox, m.Delim)
		if err := imp.importMailbox(ctx, session, pool, m.Mailbox, mboxPath, records); err != nil {
			records <- connectors.NewError(mboxPath, err)
		}
	}

	return nil
}

func hasAttr(attrs []imap.MailboxAttr, want imap.MailboxAttr) bool {
	for _, a := range attrs {
		if a == want {
			return true
		}
	}
	return false
}

// scopeMailboxes filters the full LIST result to the configured mailbox prefix
// and its descendants. With no prefix it returns the list unchanged. The
// prefix is matched against the native mailbox name built from the server's
// hierarchy delimiter, so "Archive" matches "Archive" and "Archive<delim>2024"
// but not "Archived".
func (imp *Importer) scopeMailboxes(all []*imap.ListData) ([]*imap.ListData, error) {
	if len(imp.connector.MailboxPrefix) == 0 {
		return all, nil
	}

	// Determine the server delimiter from the LIST results.
	var delim rune
	for _, m := range all {
		if m.Delim != 0 {
			delim = m.Delim
			break
		}
	}

	prefix := common.SegmentsToMailbox(imp.connector.MailboxPrefix, delim)
	var sep string
	if delim != 0 {
		sep = string(delim)
	}

	var out []*imap.ListData
	for _, m := range all {
		if m.Mailbox == prefix || (sep != "" && strings.HasPrefix(m.Mailbox, prefix+sep)) {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("mailbox %q not found on server", prefix)
	}
	return out, nil
}

func (imp *Importer) importMailbox(ctx context.Context, session *common.ImapSession, pool *common.ConnectionPool, mailbox, mboxPath string, records chan<- *connectors.Record) error {
	sel, err := session.Client.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return fmt.Errorf("SELECT %q failed: %w", mailbox, err)
	}
	if sel.NumMessages == 0 {
		return nil
	}

	// Sequence numbers 1:* (NOT UIDs)
	var seq imap.SeqSet
	seq.AddRange(1, sel.NumMessages)

	msgs, err := session.Client.Fetch(seq, &imap.FetchOptions{
		UID:          true,
		Flags:        true,
		InternalDate: true,
		RFC822Size:   true,
		Envelope:     true,
	}).Collect()
	if err != nil {
		return fmt.Errorf("FETCH %q failed: %w", mailbox, err)
	}

	for _, msg := range msgs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if msg.UID == 0 {
			continue
		}

		uid := msg.UID

		mod := time.Unix(0, 0)
		if !msg.InternalDate.IsZero() {
			mod = msg.InternalDate
		}

		size := int64(0)
		if msg.RFC822Size != 0 {
			size = msg.RFC822Size
		}

		subject := ""
		if msg.Envelope != nil {
			subject = msg.Envelope.Subject
		}

		lname := common.MessageFileName(uid, msg.Flags, subject)
		p := path.Join(mboxPath, lname)

		fi := objects.FileInfo{
			Lname:    lname,
			Lmode:    0o600,
			Lsize:    size,
			LmodTime: mod,
		}

		mbox := mailbox
		u := uid

		reader := func() (io.ReadCloser, error) {
			out, err := imp.fetchBody(ctx, pool, mbox, u)
			if err != nil {
				return nil, err
			}
			return io.NopCloser(bytes.NewReader(out)), nil
		}

		records <- connectors.NewRecord(p, "", fi, nil, reader)
	}

	return nil
}

// bodyFetchAttempts is the total number of times fetchBody will try to read a
// message body before giving up. The first attempt plus one retry covers the
// common case of a transient connection drop or a single stalled operation
// (e.g. provider throttling) without masking a genuinely unreadable message.
const bodyFetchAttempts = 2

// fetchBody reads (and copies) the full RFC822 body of a message by UID,
// retrying on transient/connection errors with a fresh pooled connection. A
// clean protocol-level error (e.g. the message was expunged) is returned
// immediately without retrying.
func (imp *Importer) fetchBody(ctx context.Context, pool *common.ConnectionPool, mbox string, uid imap.UID) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < bodyFetchAttempts; attempt++ {
		if attempt > 0 {
			// Small backoff before retrying on a fresh connection; bail out
			// promptly if the backup is being cancelled.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}

		var out []byte
		err := pool.WithSession(ctx, func(ps *common.PoolSession) error {
			// SELECT mailbox on this pooled connection (state is per-connection).
			if ps.Selected != mbox {
				if _, err := ps.Session.Client.Select(mbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
					ps.Bad = true
					return fmt.Errorf("SELECT %q failed: %w", mbox, err)
				}
				ps.Selected = mbox
			}

			// UID FETCH the full body (peek so we don't set \Seen on the source).
			var set imap.UIDSet
			set.AddNum(uid)
			fopts := &imap.FetchOptions{
				BodySection: []*imap.FetchItemBodySection{{Peek: true}},
			}

			msgs, err := ps.Session.Client.Fetch(set, fopts).Collect()
			if err != nil {
				ps.Bad = true
				return err
			}
			if len(msgs) != 1 || len(msgs[0].BodySection) != 1 {
				return fmt.Errorf("unexpected fetch response (msgs=%d sections=%d)", len(msgs), func() int {
					if len(msgs) == 0 {
						return 0
					}
					return len(msgs[0].BodySection)
				}())
			}

			out = append(out, msgs[0].BodySection[0].Bytes...) // copy so we can return after the session is back in the pool
			return nil
		})
		if err == nil {
			return out, nil
		}
		lastErr = err
		if ctx.Err() != nil || !common.IsRetryable(err) {
			break
		}
	}
	return nil, fmt.Errorf("fetch body uid %d in %q: %w", uint32(uid), mbox, lastErr)
}

func (imp *Importer) Close(ctx context.Context) error {
	if imp.pool != nil {
		imp.pool.Close()
		imp.pool = nil
	}
	return nil
}
