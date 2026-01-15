/*
 * Copyright (c) 2023 Gilles Chehade <gilles@poolp.org>
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
	"path/filepath"
	"strings"
	"sync"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

type FSExporter struct {
	opts    *connectors.Options
	rootDir string

	hlCreate singleflight.Group // key -> ensures canonical exists, returns canonical abs path
	hlCanon  sync.Map           // key -> canonical abs path string
	hlMu     sync.Map           // key -> *sync.Mutex (serialize os.Link per key)
}

func init() {
	exporter.Register("fs", location.FLAG_LOCALFS, NewFSExporter)
}

func NewFSExporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	location := config["location"]
	rootDir := strings.TrimPrefix(location, name+"://")

	return &FSExporter{
		opts:    opts,
		rootDir: rootDir,
	}, nil
}

func (p *FSExporter) Root() string          { return p.rootDir }
func (p *FSExporter) Origin() string        { return p.opts.Hostname }
func (p *FSExporter) Type() string          { return "fs" }
func (p *FSExporter) Flags() location.Flags { return location.FLAG_LOCALFS }

func (p *FSExporter) Ping(ctx context.Context) error {
	return nil
}

func (p *FSExporter) Close(ctx context.Context) error {
	return nil
}

type dirPerm struct {
	Pathname string
	Fileinfo objects.FileInfo
}

func (p *FSExporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) (ret error) {
	defer close(results)
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(p.opts.MaxConcurrency)

	dirPerms := make([]dirPerm, 0, 1024)

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

			pathname := filepath.Join(p.rootDir, record.Pathname)

			if record.FileInfo.Lmode.IsDir() {
				if err := os.Mkdir(pathname, 0700); err != nil {
					results <- record.Error(err)
				} else {
					results <- record.Ok()
				}

				// later patching
				dirPerms = append(dirPerms, dirPerm{
					Pathname: pathname,
					Fileinfo: record.FileInfo,
				})

				continue
			}

			g.Go(func() error {
				var err error
				if record.FileInfo.Lmode&os.ModeSymlink != 0 {
					err = p.symlink(record, pathname)
				} else if record.FileInfo.Lmode.IsRegular() {
					err = p.file(record, pathname)
				}

				if err != nil {
					results <- record.Error(err)
				} else {
					results <- record.Ok()
				}
				return nil
			})

		}
	}

	if err := g.Wait(); err != nil && ret == nil {
		ret = err
	}

	for i := len(dirPerms) - 1; i >= 0; i-- {
		if err := p.permissions(dirPerms[i].Pathname, dirPerms[i].Fileinfo); err != nil {
			return err
		}
	}

	return ret
}

func (p *FSExporter) symlink(record *connectors.Record, pathname string) error {
	if err := os.Symlink(record.Target, pathname); err != nil {
		return err
	}

	fileinfo := record.FileInfo

	if os.Geteuid() == 0 {
		err := os.Lchown(pathname, int(fileinfo.Uid()), int(fileinfo.Gid()))
		if err != nil {
			return err
		}
	}

	return Lutimes(pathname, fileinfo.ModTime(), fileinfo.ModTime())
}

func (p *FSExporter) hardlink(record *connectors.Record, pathname string) error {
	fileinfo := record.FileInfo
	key := fmt.Sprintf("%d:%d", fileinfo.Dev(), fileinfo.Ino())

	v, err, _ := p.hlCreate.Do(key, func() (any, error) {
		if v, ok := p.hlCanon.Load(key); ok {
			return v, nil
		}
		if err := p.writeAtomic(record, pathname); err != nil {
			return "", err
		}
		p.hlCanon.Store(key, filepath.Join(p.rootDir, pathname))
		return pathname, nil
	})
	if err != nil {
		return err
	}
	canonPath := v.(string)

	// If we are not the canonical path, create a hardlink
	if canonPath != pathname {
		if err := os.Link(canonPath, pathname); err != nil {
			return err
		}
	}

	return nil
}

func (p *FSExporter) file(record *connectors.Record, pathname string) error {
	if record.FileInfo.Lnlink > 1 {
		return p.hardlink(record, pathname)
	}
	return p.writeAtomic(record, pathname)
}

func (p *FSExporter) writeAtomic(record *connectors.Record, pathname string) error {
	tmp, err := os.CreateTemp(filepath.Dir(pathname), ".plakar-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	ok := false
	defer func() {
		if !ok {
			os.Remove(tmpName)
		}
	}()

	if _, err := io.Copy(tmp, record.Reader); err != nil {
		tmp.Close()
		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	if err := os.Rename(tmpName, pathname); err != nil {
		return err
	}

	ok = true

	fileinfo := record.FileInfo
	mode := fileinfo.Mode().Perm() | fileinfo.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)
	if err := os.Chmod(pathname, mode); err != nil {
		return err
	}

	return Lutimes(pathname, fileinfo.ModTime(), fileinfo.ModTime())
}

func (p *FSExporter) permissions(pathname string, fileinfo objects.FileInfo) error {
	if fileinfo.Mode()&os.ModeSymlink == 0 {
		// Preserve all permission bits including setuid (04000), setgid (02000), and sticky bit (01000)
		// Use the full mode which includes these special bits, not just Mode().Perm()
		mode := fileinfo.Mode().Perm() | fileinfo.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)
		if err := os.Chmod(pathname, mode); err != nil {
			return err
		}
	}
	if os.Geteuid() == 0 {
		if err := os.Lchown(pathname, int(fileinfo.Uid()), int(fileinfo.Gid())); err != nil {
			return err
		}
	}
	if err := Lutimes(pathname, fileinfo.ModTime(), fileinfo.ModTime()); err != nil {
		return err
	}
	return nil
}
