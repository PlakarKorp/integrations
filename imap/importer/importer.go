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

	"github.com/PlakarKorp/integration-imap/common"
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

	if err := imp.Ping(ctx); err != nil {
		return err
	}

	session, err := imp.connector.Connect()
	if err != nil {
		return err
	}
	defer func() { _ = session.Logout() }()

	pool, err := common.NewPool(imp.connector, 5)
	if err != nil {
		return err
	}
	defer pool.Close()

	mboxes, err := session.Client.List("", "*", nil).Collect()
	if err != nil {
		return fmt.Errorf("LIST failed: %w", err)
	}

	// root dir
	records <- connectors.NewRecord("/", "", objects.FileInfo{
		Lname:    "/",
		Lmode:    os.ModeDir | 0o700,
		Lsize:    0,
		LmodTime: time.Unix(0, 0),
	}, nil, nil)

	// mailbox dirs and parents
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
		mboxPath := path.Clean("/" + m.Mailbox)
		curr := "/"
		for part := range strings.SplitSeq(strings.TrimPrefix(mboxPath, "/"), "/") {
			if part == "" {
				continue
			}
			curr = path.Join(curr, part)
			emitDir(curr)
		}
	}

	for _, m := range mboxes {
		if err := imp.importMailbox(ctx, session, pool, m.Mailbox, records); err != nil {
			records <- connectors.NewError("/"+m.Mailbox, err)
		}
	}

	return nil
}

func (imp *Importer) importMailbox(ctx context.Context, session *common.ImapSession, pool *common.ConnectionPool, mailbox string, records chan<- *connectors.Record) error {
	sel, err := session.Client.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return fmt.Errorf("SELECT %q failed: %w", mailbox, err)
	}
	if sel.NumMessages == 0 {
		return nil
	}

	// Sequence numbers 1:* (NOT UIDs)
	var seq imap.SeqSet
	seq.AddRange(1, uint32(sel.NumMessages))

	msgs, err := session.Client.Fetch(seq, &imap.FetchOptions{
		UID:          true,
		InternalDate: true,
		RFC822Size:   true,
		Envelope:     true,
	}).Collect()
	if err != nil {
		return fmt.Errorf("FETCH (UID) %q failed: %w", mailbox, err)
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

		uid := imap.UID(msg.UID)
		uidStr := fmt.Sprint(uid)

		mod := time.Unix(0, 0)
		if !msg.InternalDate.IsZero() {
			mod = msg.InternalDate
		}

		size := int64(-1)
		if msg.RFC822Size != 0 {
			size = int64(msg.RFC822Size)
		}

		base := uidStr
		if msg.Envelope != nil && msg.Envelope.Subject != "" {
			base = fmt.Sprintf("%s-%s", uidStr, common.SafeName(msg.Envelope.Subject))
		}
		lname := base + ".eml"

		p := fmt.Sprintf("/%s/%s", mailbox, lname)

		fi := objects.FileInfo{
			Lname:    lname,
			Lmode:    0o600,
			Lsize:    size,
			LmodTime: mod,
		}

		mbox := mailbox
		u := uid

		reader := func() (io.ReadCloser, error) {
			var out []byte

			err := pool.WithSession(ctx, func(ps *common.PoolSession) error {
				// SELECT mailbox on this pooled connection (state is per-connection)
				if ps.Selected != mbox {
					if _, err := ps.Session.Client.Select(mbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
						ps.Selected = ""
						return fmt.Errorf("SELECT %q failed: %w", mbox, err)
					}
					ps.Selected = mbox
				}

				// UID FETCH
				var set imap.UIDSet
				set.AddNum(u)

				fopts := &imap.FetchOptions{
					BodySection: []*imap.FetchItemBodySection{
						{Peek: true},
					},
				}

				msgs, err := ps.Session.Client.Fetch(set, fopts).Collect()
				if err != nil {
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

				out = append(out, msgs[0].BodySection[0].Bytes...) // copy so we can return after putting session back
				return nil
			})

			if err != nil {
				return nil, err
			}
			return io.NopCloser(bytes.NewReader(out)), nil
		}

		records <- connectors.NewRecord(p, "", fi, nil, reader)
	}

	return nil
}

func (imp *Importer) Close(ctx context.Context) error {
	return nil
}
