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
	"io"
	"os"
	"path"
	"strings"
	"time"

	plakarsmb "github.com/PlakarKorp/integrations/smb/common"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
)

func init() {
	exporter.Register("smb", 0, NewExporter)
}

// share is the part of *plakarsmb.Conn the exporter uses; tests stand in an
// in-memory server for it.
type share interface {
	Lstat(p string) (os.FileInfo, error)
	Mkdir(p string, perm os.FileMode) error
	MkdirAll(p string, perm os.FileMode) error
	Create(p string) (io.WriteCloser, error)
	Symlink(target, linkpath string) error
	Chtimes(p string, mtime time.Time) error
	Close() error
}

// autoDirPerm is the mode of directories created on the way to a restored
// entry whose own directory record was not seen.
const autoDirPerm = 0o755

type Exporter struct {
	opts *connectors.Options

	cfg  *plakarsmb.Config
	conn share

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

	// The server resolves symlinks on our behalf and, without reparse point
	// support, does not even let us see them, so a directory already on the
	// share can never be told apart from a symlink to one. Records are only
	// written below the restore root and below directories this restore
	// created itself, which are collected here.
	created := make(map[string]struct{})

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

			if err := p.exportOne(record, created); err != nil {
				results <- record.Error(err)
			} else {
				results <- record.Ok()
			}
		}
	}
}

func (p *Exporter) exportOne(record *connectors.Record, created map[string]struct{}) error {
	// '\' is a path separator on the SMB server but an ordinary character to
	// path.Clean, so it would slip ".." past safe(). SMB can't represent such
	// names faithfully anyway.
	if strings.Contains(record.Pathname, `\`) || strings.Contains(record.Target, `\`) {
		return fmt.Errorf("refusing backslash in pathname or target: %q", record.Pathname)
	}

	root := path.Clean(p.rootDir)
	pathname := path.Join(p.rootDir, record.Pathname)
	if !p.safe(pathname) {
		return fmt.Errorf("refusing to write outside share root: %q", pathname)
	}
	mode := record.FileInfo.Mode()
	if !mode.IsDir() && pathname == root {
		return fmt.Errorf("refusing non-directory record at share root: %q", pathname)
	}
	if mode&os.ModeSymlink != 0 {
		if err := p.safeTarget(record.Target, pathname); err != nil {
			return err
		}
	}

	if err := p.parents(pathname, created); err != nil {
		return err
	}

	switch {
	case mode.IsDir():
		if pathname == root {
			// The root is the user's target: create it if it is missing,
			// take it as it is otherwise.
			return p.conn.MkdirAll(pathname, mode.Perm())
		}
		return p.mkdir(pathname, mode.Perm(), created)
	case mode&os.ModeSymlink != 0:
		return p.symlink(record, pathname)
	case mode.IsRegular():
		return p.writeFile(record, pathname)
	default:
		// devices, sockets, fifos have no SMB representation.
		return fmt.Errorf("smb: unsupported file type %q for %s", mode.String(), pathname)
	}
}

// parents makes sure every directory between the restore root and pathname
// was created by this restore, creating the missing ones. The root itself is
// trusted as given.
func (p *Exporter) parents(pathname string, created map[string]struct{}) error {
	root := path.Clean(p.rootDir)
	if pathname == root {
		return nil
	}

	var missing []string
	for dir := path.Dir(pathname); dir != root && dir != "/"; dir = path.Dir(dir) {
		if _, ok := created[dir]; ok {
			break
		}
		missing = append(missing, dir)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := p.mkdir(missing[i], autoDirPerm, created); err != nil {
			return err
		}
	}
	return nil
}

// mkdir creates the directory at pathname and records it as ours. Anything
// already there is refused unless this restore created it: it may be a
// symlink the server would resolve outside the root.
func (p *Exporter) mkdir(pathname string, perm os.FileMode, created map[string]struct{}) error {
	if _, ok := created[pathname]; ok {
		return nil
	}
	if err := p.fresh(pathname); err != nil {
		return err
	}
	if err := p.conn.Mkdir(pathname, perm); err != nil {
		if errors.Is(err, os.ErrExist) {
			return existsErr(pathname)
		}
		return fmt.Errorf("mkdir %q: %w", pathname, err)
	}
	created[pathname] = struct{}{}
	return nil
}

func (p *Exporter) symlink(record *connectors.Record, pathname string) error {
	if err := p.conn.Symlink(record.Target, pathname); err != nil {
		if errors.Is(err, os.ErrExist) {
			return existsErr(pathname)
		}
		return fmt.Errorf("symlink %q: %w", pathname, err)
	}
	return nil
}

func (p *Exporter) writeFile(record *connectors.Record, pathname string) error {
	if err := p.fresh(pathname); err != nil {
		return err
	}

	f, err := p.conn.Create(pathname)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return existsErr(pathname)
		}
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

// fresh refuses pathname when something already sits there. Only the direct
// children of the restore root can: everything deeper lives in a directory
// this restore created. The exclusive create that follows would fail on an
// existing entry too, but a server that follows symlinks on create would
// first create the target of a dangling one; Lstat sees the link where the
// server reports reparse points, and the exclusive create catches it where
// it does not.
func (p *Exporter) fresh(pathname string) error {
	if path.Dir(pathname) != path.Clean(p.rootDir) {
		return nil
	}
	_, err := p.conn.Lstat(pathname)
	switch {
	case err == nil:
		return existsErr(pathname)
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("lstat %q: %w", pathname, err)
	}
}

func existsErr(pathname string) error {
	return fmt.Errorf("refusing to restore over %q: it exists and was not created by this restore (it may be a symlink); restore into an empty directory", pathname)
}

// safeTarget refuses a symlink target that lexically points outside the
// restore root. The check does not follow other symlinks along the target, so
// a chain of links within the root can still end up outside it; the restore
// never writes through such a link, it only leaves it behind. An absolute
// target is refused outright: go-smb2 hands it to the server as a
// volume-absolute NT path, which is not the Unix path the snapshot recorded.
func (p *Exporter) safeTarget(target, linkpath string) error {
	if path.IsAbs(target) || (len(target) >= 2 && target[1] == ':') {
		return fmt.Errorf("refusing absolute symlink target %q for %q", target, linkpath)
	}
	if !p.safe(path.Join(path.Dir(linkpath), target)) {
		return fmt.Errorf("refusing symlink target %q escaping the restore root for %q", target, linkpath)
	}
	return nil
}

// safe guards against records carrying ".." segments (or an unclean root)
// that would escape the share root.
func (p *Exporter) safe(pathname string) bool {
	root := path.Clean(p.rootDir)
	clean := path.Clean(pathname)

	if clean == root {
		return true
	}

	prefix := root
	if prefix != "/" {
		prefix += "/"
	}
	return strings.HasPrefix(clean, prefix)
}
