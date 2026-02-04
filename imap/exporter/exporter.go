package exporter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/PlakarKorp/integration-imap/common"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
	"github.com/emersion/go-imap/v2"
	"golang.org/x/sync/errgroup"
)

func init() {
	exporter.Register("imap", 0, NewExporter)
}

type Exporter struct {
	connector common.ImapConnector
	pool      *common.ConnectionPool
	poolSize  int
}

func NewExporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	exp := &Exporter{poolSize: 5}
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

func (p *Exporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) (ret error) {
	defer close(results)

	if err := p.Ping(ctx); err != nil {
		return err
	}

	pool, err := common.NewPool(p.connector, 1)
	if err != nil {
		return err
	}
	p.pool = pool

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(1)

loop:
	for {
		select {
		case <-ctx.Done():
			ret = ctx.Err()
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

			// Normalize incoming path: may or may not start with "/"
			recPath := cleanAbs(record.Pathname)

			// directories are handled synchronously (like FTP exporter)
			if record.FileInfo.Lmode.IsDir() {
				if recPath == "/" {
					results <- record.Ok()
					continue
				}
				mbox := strings.TrimPrefix(recPath, "/")
				if err := p.ensureMailbox(ctx, mbox); err != nil {
					results <- record.Error(err)
				} else {
					results <- record.Ok()
				}
				continue
			}

			// non-dirs handled concurrently
			r := record
			g.Go(func() error {
				err := p.exportFile(ctx, r)
				if err != nil {
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

func (p *Exporter) exportFile(ctx context.Context, record *connectors.Record) error {
	if record.FileInfo.Lmode&0o120000 != 0 { // os.ModeSymlink
		return errors.ErrUnsupported
	}

	// derive mailbox from path: /Trash/xxx.eml -> Trash
	recPath := cleanAbs(record.Pathname)
	mbox, err := mailboxFromPath(recPath)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, record.Reader); err != nil {
		return err
	}
	msg := buf.Bytes()

	var aopts imap.AppendOptions
	if !record.FileInfo.LmodTime.IsZero() {
		aopts.Time = record.FileInfo.LmodTime
	} else {
		aopts.Time = time.Now()
	}

	err = p.appendMessage(ctx, mbox, msg, &aopts)
	if err == nil {
		return nil
	}
	if isTryCreate(err) {
		if e := p.ensureMailbox(ctx, mbox); e != nil {
			return fmt.Errorf("append TRYCREATE but ensure mailbox %q failed: %w (orig: %v)", mbox, e, err)
		}
		return p.appendMessage(ctx, mbox, msg, &aopts)
	}
	return err
}
func (p *Exporter) appendMessage(ctx context.Context, mailbox string, msg []byte, opts *imap.AppendOptions) error {
	do := func() error {
		return p.pool.WithSession(ctx, func(ps *common.PoolSession) error {
			if err := ps.Session.Client.Noop().Wait(); err != nil {
				ps.Bad = true
				return err
			}

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
			_, err := cmd.Wait()
			if err != nil {
				ps.Bad = true
			}
			return err
		})
	}

	// retry once after reconnect if the first attempt killed the connection
	if err := do(); err != nil {
		return do()
	}
	return nil
}

func (p *Exporter) ensureMailbox(ctx context.Context, mailbox string) error {
	mailbox = strings.Trim(mailbox, "/")
	if mailbox == "" {
		return nil
	}

	parts := strings.Split(mailbox, "/")
	curr := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		if curr == "" {
			curr = part
		} else {
			curr = curr + "/" + part
		}
		if err := p.createMailbox(ctx, curr); err != nil {
			return err
		}
	}
	return nil
}

func (exp *Exporter) createMailbox(ctx context.Context, mailbox string) error {
	return exp.pool.WithSession(ctx, func(ps *common.PoolSession) error {
		err := ps.Session.Client.Create(mailbox, nil).Wait()
		if err == nil {
			return nil
		}
		if strings.Contains(strings.ToUpper(err.Error()), "ALREADY") ||
			strings.Contains(strings.ToUpper(err.Error()), "EXISTS") {
			return nil
		}
		return err
	})
}

/* ---------- helpers (adapt to your actual Record shape) ---------- */

func cleanAbs(p string) string {
	if p == "" {
		return "/"
	}
	p = path.Clean("/" + strings.TrimPrefix(p, "/"))
	return p
}

func mailboxFromPath(p string) (string, error) {
	p = strings.TrimPrefix(p, "/")
	parts := strings.SplitN(p, "/", 2)
	if len(parts) < 1 || parts[0] == "" {
		return "", fmt.Errorf("invalid imap path: %q", p)
	}
	return parts[0], nil
}

func isTryCreate(err error) bool {
	if err == nil {
		return false
	}
	up := strings.ToUpper(err.Error())
	return strings.Contains(up, "[TRYCREATE]") || strings.Contains(up, "TRYCREATE")
}
