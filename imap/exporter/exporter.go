package exporter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/PlakarKorp/integrations/imap/common"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
	"github.com/emersion/go-imap/v2"
	"golang.org/x/sync/errgroup"
)

const defaultPoolSize = 5

func init() {
	exporter.Register("imap", 0, NewExporter)
}

type Exporter struct {
	connector common.ImapConnector
	pool      *common.ConnectionPool
	poolSize  int

	delimOnce sync.Once
	delim     rune

	mu      sync.Mutex
	created map[string]struct{} // mailboxes we have already ensured exist
}

func NewExporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	exp := &Exporter{
		poolSize: defaultPoolSize,
		created:  make(map[string]struct{}),
	}
	if err := exp.connector.InitFromConfig(config); err != nil {
		return nil, err
	}
	return exp, nil
}

func (exp *Exporter) Root() string          { return "/" }
func (exp *Exporter) Origin() string        { return exp.connector.Address }
func (exp *Exporter) Type() string          { return "imap" }
func (exp *Exporter) Flags() location.Flags { return 0 }

func (exp *Exporter) Ping(ctx context.Context) error {
	s, err := exp.connector.Connect()
	if err != nil {
		return err
	}
	defer func() { _ = s.Logout() }()

	_, err = s.Client.List("", "*", nil).Collect()
	return err
}

func (exp *Exporter) Close(ctx context.Context) error {
	if exp.pool != nil {
		exp.pool.Close()
	}
	return nil
}

// delimiter discovers (once) the destination server's hierarchy delimiter so
// kloset paths can be mapped back to native mailbox names.
func (exp *Exporter) delimiter(ctx context.Context) rune {
	exp.delimOnce.Do(func() {
		exp.delim = '/' // sane default
		_ = exp.pool.WithSession(ctx, func(ps *common.PoolSession) error {
			list, err := ps.Session.Client.List("", "", &imap.ListOptions{}).Collect()
			if err == nil {
				for _, l := range list {
					if l.Delim != 0 {
						exp.delim = l.Delim
						break
					}
				}
			}
			return nil
		})
	})
	return exp.delim
}

func (exp *Exporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) (ret error) {
	defer close(results)

	if err := exp.Ping(ctx); err != nil {
		return err
	}

	pool, err := common.NewPool(exp.connector, exp.poolSize)
	if err != nil {
		return err
	}
	exp.pool = pool

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(exp.poolSize)

loop:
	for {
		select {
		case <-gctx.Done():
			ret = gctx.Err()
			break loop

		case record, ok := <-records:
			if !ok {
				break loop
			}

			if record.Err != nil {
				results <- record.Ok()
				continue
			}
			if record.IsXattr {
				results <- record.Ok()
				continue
			}

			recPath := record.Pathname

			// Directories map to mailboxes; create them synchronously so a
			// message APPEND never races ahead of its parent mailbox.
			if record.FileInfo.Lmode.IsDir() {
				mbox, err := common.PathToMailbox(recPath, exp.delimiter(gctx))
				if err != nil {
					results <- record.Error(err)
					continue
				}
				if mbox == "" {
					results <- record.Ok()
					continue
				}
				if err := exp.ensureMailbox(gctx, mbox); err != nil {
					results <- record.Error(err)
				} else {
					results <- record.Ok()
				}
				continue
			}

			r := record
			g.Go(func() error {
				if err := exp.exportFile(gctx, r); err != nil {
					results <- r.Error(err)
				} else {
					results <- r.Ok()
				}
				return nil
			})
		}
	}

	if err := g.Wait(); err != nil && ret == nil {
		ret = err
	}
	return ret
}

func (exp *Exporter) exportFile(ctx context.Context, record *connectors.Record) error {
	if record.FileInfo.Lmode&0o120000 != 0 { // os.ModeSymlink
		return errors.ErrUnsupported
	}

	// The mailbox is the directory portion of the path; the file name carries
	// the original flags.
	dir, file := splitPath(record.Pathname)
	mbox, err := common.PathToMailbox(dir, exp.delimiter(ctx))
	if err != nil {
		return err
	}
	if mbox == "" {
		return fmt.Errorf("refusing to restore message at repository root: %q", record.Pathname)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, record.Reader); err != nil {
		return err
	}
	msg := buf.Bytes()

	aopts := &imap.AppendOptions{
		Flags: common.ParseMessageFileName(file),
	}
	if !record.FileInfo.LmodTime.IsZero() {
		aopts.Time = record.FileInfo.LmodTime
	} else {
		aopts.Time = time.Now()
	}

	err = exp.appendMessage(ctx, mbox, msg, aopts)
	if err == nil {
		return nil
	}
	if common.IsTryCreate(err) {
		if e := exp.ensureMailbox(ctx, mbox); e != nil {
			return fmt.Errorf("append to %q returned TRYCREATE but ensuring mailbox failed: %w (orig: %v)", mbox, e, err)
		}
		return exp.appendMessage(ctx, mbox, msg, aopts)
	}
	return err
}

func (exp *Exporter) appendMessage(ctx context.Context, mailbox string, msg []byte, opts *imap.AppendOptions) error {
	attempt := func() error {
		return exp.pool.WithSession(ctx, func(ps *common.PoolSession) error {
			cmd := ps.Session.Client.Append(mailbox, int64(len(msg)), opts)
			if _, err := cmd.Write(msg); err != nil {
				_ = cmd.Close()
				ps.Bad = true
				return err
			}
			if err := cmd.Close(); err != nil {
				ps.Bad = true
				return err
			}
			if _, err := cmd.Wait(); err != nil {
				// A TRYCREATE is a clean protocol response, not a broken
				// connection: keep the session for the caller's retry.
				if !common.IsTryCreate(err) {
					ps.Bad = true
				}
				return err
			}
			return nil
		})
	}

	err := attempt()
	if err == nil || common.IsTryCreate(err) {
		return err
	}
	// One retry on a (now reconnected) session for transient connection errors.
	return attempt()
}

func (exp *Exporter) ensureMailbox(ctx context.Context, mailbox string) error {
	mailbox = strings.TrimSpace(mailbox)
	if mailbox == "" {
		return nil
	}

	exp.mu.Lock()
	if _, ok := exp.created[mailbox]; ok {
		exp.mu.Unlock()
		return nil
	}
	exp.mu.Unlock()

	// Create the full hierarchy. Intermediate mailboxes are created with the
	// destination delimiter so nested folders land in the right place.
	delim := exp.delimiter(ctx)
	segs := strings.Split(mailbox, string(delim))
	curr := ""
	for _, part := range segs {
		if part == "" {
			continue
		}
		if curr == "" {
			curr = part
		} else {
			curr = curr + string(delim) + part
		}
		if err := exp.createMailbox(ctx, curr); err != nil {
			return err
		}
	}

	exp.mu.Lock()
	exp.created[mailbox] = struct{}{}
	exp.mu.Unlock()
	return nil
}

func (exp *Exporter) createMailbox(ctx context.Context, mailbox string) error {
	return exp.pool.WithSession(ctx, func(ps *common.PoolSession) error {
		err := ps.Session.Client.Create(mailbox, nil).Wait()
		if err == nil || common.IsAlreadyExists(err) {
			return nil
		}
		return err
	})
}

func splitPath(p string) (dir, file string) {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return "/", p
	}
	dir = p[:i]
	if dir == "" {
		dir = "/"
	}
	return dir, p[i+1:]
}
