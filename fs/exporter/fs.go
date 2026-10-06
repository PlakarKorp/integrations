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
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
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

	root *os.Root

	hlCreate singleflight.Group // key -> ensures canonical exists, returns hlCanonical
	hlCanon  sync.Map           // key -> hlCanonical

	skipRootPermsAndTime bool // we sometimes can't restore them when on a mountpoint, needed for openshift
	skipOwnership        bool
	skipPerms            bool
	skipTimes            bool
}

func init() {
	exporter.Register("fs", location.FLAG_LOCALFS, NewFSExporter)
}

func NewFSExporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	location := config["location"]
	rootDir := strings.TrimPrefix(location, name+"://")

	skipRootPermsAndTime := false
	if v, ok := config["skip_root_perms_and_time"]; ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("invalid value for %s: %w", "skip_root_perms_and_time", err)
		}

		skipRootPermsAndTime = b
	}
	skipOwnership := false
	if v, ok := config["skip_ownership"]; ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("invalid value for %s: %w", "skip_ownership", err)
		}

		skipOwnership = b
	}

	skipPermissions := false
	if v, ok := config["skip_permissions"]; ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("invalid value for %s: %w", "skip_permissions", err)
		}

		skipPermissions = b
	}

	skipTimes := false
	if v, ok := config["skip_times"]; ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("invalid value for %s: %w", "skip_times", err)
		}

		skipTimes = b
	}

	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("failed to absolutify root: %w", err)
	}

	// The restore target has to exist before it can be opened as a root.
	// Records restore into it, they no longer create it.
	if err := os.MkdirAll(absRoot, 0700); err != nil {
		return nil, fmt.Errorf("failed to create restore root %s: %w", absRoot, err)
	}

	root, err := os.OpenRoot(absRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to open restore root %s: %w", absRoot, err)
	}

	return &FSExporter{
		opts:                 opts,
		rootDir:              absRoot,
		root:                 root,
		skipRootPermsAndTime: skipRootPermsAndTime,
		skipOwnership:        skipOwnership,
		skipPerms:            skipPermissions,
		skipTimes:            skipTimes,
	}, nil
}

func relative(pathname string) (string, error) {
	p := path.Clean("/" + pathname)

	if p != pathname {
		return "", fmt.Errorf("record path is not clean %q", pathname)
	}

	if p == "/" {
		return ".", nil
	}

	// Strip the leading / so that it's relative to the root
	path, err := filepath.Localize(p[1:])
	if err != nil {
		return "", fmt.Errorf("localize: %q: %w", pathname, err)
	}

	return path, nil
}

func (p *FSExporter) Root() string          { return p.rootDir }
func (p *FSExporter) Origin() string        { return p.opts.Hostname }
func (p *FSExporter) Type() string          { return "fs" }
func (p *FSExporter) Flags() location.Flags { return location.FLAG_LOCALFS }

func (p *FSExporter) Ping(ctx context.Context) error {
	return nil
}

func (p *FSExporter) Close(ctx context.Context) error {
	return p.root.Close()
}

type hlCanonical struct {
	pathname string
	sum      [sha256.Size]byte
}

type dirPerm struct {
	Pathname string
	Fileinfo objects.FileInfo
}

func (p *FSExporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) (ret error) {
	defer close(results)

	var g errgroup.Group
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

			pathname, err := relative(record.Pathname)
			if err != nil {
				results <- record.Error(err)
				continue
			}

			if record.FileInfo.Lmode.IsDir() {
				if err := p.root.Mkdir(pathname, 0700); err != nil {
					if !os.IsExist(err) {
						results <- record.Error(err)
					} else {
						if !p.skipPerms {
							_ = p.root.Chmod(pathname, 0700)
						}
						results <- record.Ok()
					}
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

	var failures int
	for i := len(dirPerms) - 1; i >= 0; i-- {
		if err := p.permissions(dirPerms[i].Pathname, dirPerms[i].Fileinfo); err != nil {
			// keep trying to set permissions even if
			// something failed; keep a maximum number of
			// errors to avoid an huge error message if
			// things are completely broken.
			if failures++; failures <= 10 {
				ret = errors.Join(ret, err)
			}
		}
	}

	return ret
}

func (p *FSExporter) symlink(record *connectors.Record, pathname string) error {
	if err := p.root.Symlink(record.Target, pathname); err != nil {
		return err
	}

	fileinfo := record.FileInfo

	if os.Geteuid() == 0 && !p.skipOwnership {
		err := p.root.Lchown(pathname, int(fileinfo.Uid()), int(fileinfo.Gid()))
		if err != nil {
			return err
		}
	}

	if !p.skipTimes {
		// This is safe to do through the real filesystem because pathname has been
		// validated already through root.Symlink()
		realpath := filepath.Join(p.root.Name(), pathname)
		return Lutimes(realpath, fileinfo.ModTime(), fileinfo.ModTime())
	}

	return nil
}

func (p *FSExporter) hardlink(record *connectors.Record, pathname string) error {
	fileinfo := record.FileInfo
	key := fmt.Sprintf("%d:%d", fileinfo.Dev(), fileinfo.Ino())

	v, err, _ := p.hlCreate.Do(key, func() (any, error) {
		if v, ok := p.hlCanon.Load(key); ok {
			return v, nil
		}
		tmpName, sum, err := p.writeTemp(record, pathname)
		if err != nil {
			return nil, err
		}
		if err := p.commit(tmpName, pathname, fileinfo); err != nil {
			return nil, err
		}
		canon := hlCanonical{pathname: pathname, sum: sum}
		p.hlCanon.Store(key, canon)
		return canon, nil
	})
	if err != nil {
		return err
	}
	canon := v.(hlCanonical)

	if canon.pathname == pathname {
		return nil
	}

	// dev:ino comes from untrusted snapshot metadata, only link if the content matches.
	tmpName, sum, err := p.writeTemp(record, pathname)
	if err != nil {
		return err
	}
	if sum != canon.sum {
		_ = p.root.Remove(tmpName)
		return fmt.Errorf("hardlink %q: content does not match canonical file %q: dev:ino metadata is forged", pathname, canon.pathname)
	}
	if err := p.root.Remove(tmpName); err != nil {
		return err
	}
	return p.root.Link(canon.pathname, pathname)
}

func (p *FSExporter) file(record *connectors.Record, pathname string) error {
	if record.FileInfo.Lnlink > 1 {
		return p.hardlink(record, pathname)
	}
	return p.writeAtomic(record, pathname)
}

// createTemp is os.CreateTemp confined to the restore root.  The name is drawn
// from crypto/rand so a concurrent writer on the same directory cannot predict
// and pre-create it; O_EXCL means we lose the race loudly rather than silently
// writing into someone else's file.
func (p *FSExporter) createTemp(dir string) (*os.File, string, error) {
	var buf [10]byte
	for range 1000 {
		if _, err := rand.Read(buf[:]); err != nil {
			return nil, "", err
		}
		suffix := base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(buf[:])
		name := filepath.Join(dir, ".plakar-"+suffix)

		f, err := p.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		return f, name, nil
	}
	return nil, "", fmt.Errorf("could not create a temporary file in %s", dir)
}

func (p *FSExporter) writeAtomic(record *connectors.Record, pathname string) error {
	tmpName, _, err := p.writeTemp(record, pathname)
	if err != nil {
		return err
	}
	return p.commit(tmpName, pathname, record.FileInfo)
}

func (p *FSExporter) writeTemp(record *connectors.Record, pathname string) (string, [sha256.Size]byte, error) {
	var sum [sha256.Size]byte

	tmp, tmpName, err := p.createTemp(filepath.Dir(pathname))
	if err != nil {
		return "", sum, err
	}

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), record.Reader); err != nil {
		_ = tmp.Close()
		_ = p.root.Remove(tmpName)
		return "", sum, err
	}

	if err := tmp.Close(); err != nil {
		_ = p.root.Remove(tmpName)
		return "", sum, err
	}

	h.Sum(sum[:0])
	return tmpName, sum, nil
}

func (p *FSExporter) commit(tmpName, pathname string, fileinfo objects.FileInfo) error {
	if err := p.root.Rename(tmpName, pathname); err != nil {
		_ = p.root.Remove(tmpName)
		return err
	}
	return p.permissions(pathname, fileinfo)
}

func (p *FSExporter) permissions(pathname string, fileinfo objects.FileInfo) error {
	if pathname == "." && p.skipRootPermsAndTime {
		return nil
	}

	// chown clears setuid/setgid, so it must come before chmod.
	if os.Geteuid() == 0 && !p.skipOwnership {
		if err := p.root.Lchown(pathname, int(fileinfo.Uid()), int(fileinfo.Gid())); err != nil {
			return fmt.Errorf("chown(%s): %w", pathname, err)
		}
	}

	if fileinfo.Mode()&os.ModeSymlink == 0 && !p.skipPerms {
		// Preserve all permission bits including setuid (04000), setgid (02000), and sticky bit (01000)
		// Use the full mode which includes these special bits, not just Mode().Perm()
		mode := fileinfo.Mode().Perm() | fileinfo.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)
		if err := p.root.Chmod(pathname, mode); err != nil {
			return fmt.Errorf("chmod(%s): %w", pathname, err)
		}
	}

	if !p.skipTimes {
		// This is safe to do through the real filesystem because pathname has been
		// validated already through either through Mkdir for a directory or Rename
		// for a file or an hardlink.
		realpath := filepath.Join(p.root.Name(), pathname)
		if err := Lutimes(realpath, fileinfo.ModTime(), fileinfo.ModTime()); err != nil {
			return fmt.Errorf("lutimes(%s): %w", pathname, err)
		}
	}

	return nil
}
