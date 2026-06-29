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
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	plakarnfs "github.com/PlakarKorp/integrations/nfs/common"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
)

func init() {
	exporter.Register("nfs", 0, NewExporter)
}

// errUnsupportedType is reported for entries NFSv3-over-RPC cannot reconstruct
// through this connector (symlinks, devices, sockets, fifos): the client
// library exposes neither SYMLINK nor MKNOD.
var errUnsupportedType = errors.New("nfs: unsupported file type for export (only regular files and directories are restored)")

type Exporter struct {
	opts *connectors.Options

	cfg  *plakarnfs.Config
	conn *plakarnfs.Conn

	rootDir string
}

func NewExporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	cfg, err := plakarnfs.ParseConfig(config)
	if err != nil {
		return nil, err
	}

	conn, err := plakarnfs.Connect(cfg)
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
func (p *Exporter) Type() string          { return "nfs" }
func (p *Exporter) Flags() location.Flags { return 0 }

func (p *Exporter) Ping(ctx context.Context) error {
	_, _, err := p.conn.Lookup(p.rootDir)
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
		return fmt.Errorf("refusing to write outside export: %q", pathname)
	}
	mode := record.FileInfo.Mode()

	switch {
	case mode.IsDir():
		return p.mkdirAll(pathname, mode.Perm())
	case mode.IsRegular():
		return p.writeFile(record, pathname, mode.Perm())
	default:
		// symlinks, devices, sockets, fifos: not reconstructable over NFSv3
		// with the current client library.
		return errUnsupportedType
	}
}

// mkdirAll creates pathname and any missing parents. NFS MKDIR fails if the
// directory already exists, so an existing directory is treated as success.
func (p *Exporter) mkdirAll(pathname string, perm os.FileMode) error {
	if pathname == "" || pathname == "/" {
		return nil
	}
	if p.isDir(pathname) {
		return nil
	}
	if parent := path.Dir(pathname); parent != pathname {
		if err := p.mkdirAll(parent, perm); err != nil {
			return err
		}
	}
	if err := p.conn.Mkdir(pathname, perm); err != nil {
		// A concurrent create or a pre-existing directory is fine.
		if p.isDir(pathname) {
			return nil
		}
		return fmt.Errorf("mkdir %q: %w", pathname, err)
	}
	return nil
}

func (p *Exporter) writeFile(record *connectors.Record, pathname string, perm os.FileMode) error {
	if err := p.mkdirAll(path.Dir(pathname), 0o755); err != nil {
		return err
	}

	if err := p.conn.WriteFile(pathname, perm, record.Reader); err != nil {
		return fmt.Errorf("write %q: %w", pathname, err)
	}
	return nil
}

func (p *Exporter) isDir(pathname string) bool {
	attr, _, err := p.conn.Lookup(pathname)
	if err != nil || attr == nil {
		return false
	}
	return plakarnfs.IsDir(attr)
}

// ensure the path stays inside the mounted export; defensive against records
// carrying ".." segments.
func (p *Exporter) safe(pathname string) bool {
	clean := path.Clean(pathname)
	return clean == p.rootDir || strings.HasPrefix(clean, strings.TrimRight(p.rootDir, "/")+"/")
}
