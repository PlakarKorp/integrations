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
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	plakarsftp "github.com/PlakarKorp/integration-sftp/common"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/pkg/sftp"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

func init() {
	exporter.Register("sftp", 0, NewExporter)
}

type Exporter struct {
	opts *connectors.Options

	client   *sftp.Client
	endpoint *url.URL

	hlCreate singleflight.Group // key -> ensures canonical exists, returns canonical abs path
	hlCanon  sync.Map           // key -> canonical abs path string
	hlMu     sync.Map           // key -> *sync.Mutex (serialize os.Link per key)
}

func NewExporter(ctx context.Context, opt *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	var err error

	target := config["location"]

	parsed, err := url.Parse(target)
	if err != nil {
		return nil, err
	}

	client, err := plakarsftp.Connect(parsed, config)
	if err != nil {
		return nil, err
	}

	return &Exporter{
		opts:     opt,
		endpoint: parsed,
		client:   client,
	}, nil
}

func (p *Exporter) Root() string          { return p.endpoint.Path }
func (p *Exporter) Origin() string        { return p.endpoint.Host }
func (p *Exporter) Type() string          { return "sftp" }
func (p *Exporter) Flags() location.Flags { return 0 }

func (p *Exporter) Ping(ctx context.Context) error {
	return nil
}

func (p *Exporter) Close(ctx context.Context) error {
	return nil
}

type dirPerm struct {
	Pathname string
	Fileinfo objects.FileInfo
}

func (p *Exporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) (ret error) {
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

			pathname := filepath.Join(p.Root(), record.Pathname)
			if record.FileInfo.Lmode.IsDir() {
				if err := p.client.Mkdir(pathname); err != nil {
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

func (p *Exporter) symlink(record *connectors.Record, pathname string) error {
	if err := p.client.Symlink(record.Target, pathname); err != nil {
		return fmt.Errorf("could not create symlink")
	}
	return nil
}

func (p *Exporter) hardlink(record *connectors.Record, pathname string) error {
	fileinfo := record.FileInfo
	key := fmt.Sprintf("%d:%d", fileinfo.Dev(), fileinfo.Ino())

	v, err, _ := p.hlCreate.Do(key, func() (any, error) {
		if v, ok := p.hlCanon.Load(key); ok {
			return v, nil
		}
		if err := p.writeAtomic(record, pathname); err != nil {
			return "", err
		}
		p.hlCanon.Store(key, filepath.Join(p.Root(), pathname))
		return pathname, nil
	})
	if err != nil {
		return err
	}
	canonPath := v.(string)

	// If we are not the canonical path, create a hardlink
	if canonPath != pathname {
		if err := p.client.Link(canonPath, pathname); err != nil {
			return fmt.Errorf("could not create hardink")
		}
	}

	return nil
}

func (p *Exporter) file(record *connectors.Record, pathname string) error {
	if record.FileInfo.Lnlink > 1 {
		return p.hardlink(record, pathname)
	}
	return p.writeAtomic(record, pathname)
}

func (p *Exporter) writeAtomic(record *connectors.Record, pathname string) error {
	tmpName := fmt.Sprintf("%s.tmp.%d", pathname, rand.Int())

	tmp, err := p.client.Create(tmpName)
	if err != nil {
		return fmt.Errorf("could not create temporary file")
	}

	ok := false
	defer func() {
		if !ok {
			p.client.Remove(tmpName)
		}
	}()

	if _, err := io.Copy(tmp, record.Reader); err != nil {
		tmp.Close()
		return fmt.Errorf("could not write")
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("could not close")
	}

	if err := p.client.Rename(tmpName, pathname); err != nil {
		return fmt.Errorf("could not create")
	}

	ok = true

	fileinfo := record.FileInfo
	mode := fileinfo.Mode().Perm() | fileinfo.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)
	if err := p.client.Chmod(pathname, mode); err != nil {
		return fmt.Errorf("could not chmod")
	}
	return nil
}

func (p *Exporter) permissions(pathname string, fileinfo objects.FileInfo) error {
	if fileinfo.Mode()&os.ModeSymlink == 0 {
		// Preserve all permission bits including setuid (04000), setgid (02000), and sticky bit (01000)
		// Use the full mode which includes these special bits, not just Mode().Perm()
		mode := fileinfo.Mode().Perm() | fileinfo.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)
		if err := p.client.Chmod(pathname, mode); err != nil {
			return fmt.Errorf("could not chmod")
		}
	}
	return nil
}
