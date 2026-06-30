/*
 * Copyright (c) 2025 Gilles Chehade <gilles@poolp.org>
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */

package exporter

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	plakarsmb "github.com/PlakarKorp/integrations/smb/common"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
)

func init() {
	exporter.Register("smb", 0, NewExporter)
}

type Exporter struct {
	opts *connectors.Options

	cfg  *plakarsmb.Config
	conn *plakarsmb.Conn

	rootDir string
}

func NewExporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	cfg, err := plakarsmb.ParseConfig(config)
	if err != nil {
		return nil, err
	}

	conn, err := plakarsmb.Connect(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return &Exporter{
		opts:    opts,
		cfg:     cfg,
		conn:    conn,
		rootDir: cfg.Root,
	}, nil
}

func (p *Exporter) Root() string          { return p.rootDir }
func (p *Exporter) Origin() string        { return p.cfg.Origin() }
func (p *Exporter) Type() string          { return "smb" }
func (p *Exporter) Flags() location.Flags { return 0 }

func (p *Exporter) Ping(ctx context.Context) error {
	_, err := p.conn.Lstat(p.rootDir)
	return err
}

func (p *Exporter) Close(ctx context.Context) error {
	return p.conn.Close()
}

func (p *Exporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) (ret error) {
	defer close(results)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case record, ok := <-records:
			if !ok {
				return ret
			}

			if record.Err != nil || record.IsXattr {
				results <- record.Ok()
				continue
			}

			if err := p.exportOne(record); err != nil {
				results <- record.Error(err)
			} else {
				results <- record.Ok()
			}
		}
	}
}

func (p *Exporter) exportOne(record *connectors.Record) error {
	pathname := path.Join(p.rootDir, record.Pathname)
	if !p.safe(pathname) {
		return fmt.Errorf("refusing to write outside share root: %q", pathname)
	}
	mode := record.FileInfo.Mode()

	switch {
	case mode.IsDir():
		return p.conn.MkdirAll(pathname, mode.Perm())
	case mode&os.ModeSymlink != 0:
		return p.symlink(record, pathname)
	case mode.IsRegular():
		return p.writeFile(record, pathname)
	default:
		// devices, sockets, fifos have no SMB representation.
		return fmt.Errorf("smb: unsupported file type %q for %s", mode.String(), pathname)
	}
}

func (p *Exporter) symlink(record *connectors.Record, pathname string) error {
	if err := p.conn.MkdirAll(path.Dir(pathname), 0o755); err != nil {
		return err
	}
	if err := p.conn.Symlink(record.Target, pathname); err != nil {
		return fmt.Errorf("symlink %q: %w", pathname, err)
	}
	return nil
}

func (p *Exporter) writeFile(record *connectors.Record, pathname string) error {
	if err := p.conn.MkdirAll(path.Dir(pathname), 0o755); err != nil {
		return err
	}

	f, err := p.conn.Create(pathname)
	if err != nil {
		return fmt.Errorf("create %q: %w", pathname, err)
	}

	if _, err := io.Copy(f, record.Reader); err != nil {
		f.Close()
		return fmt.Errorf("write %q: %w", pathname, err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("close %q: %w", pathname, err)
	}

	// Best-effort: preserve modification time. SMB has no Unix mode/ownership,
	// so permission bits are not restored.
	_ = p.conn.Chtimes(pathname, record.FileInfo.ModTime())
	return nil
}

// safe guards against records carrying ".." segments that would escape the
// share root.
func (p *Exporter) safe(pathname string) bool {
	clean := path.Clean(pathname)
	root := strings.TrimRight(p.rootDir, "/")
	return clean == p.rootDir || root == "" || strings.HasPrefix(clean, root+"/")
}
