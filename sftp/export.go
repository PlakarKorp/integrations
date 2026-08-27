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

package sftp

import (
	"context"
	"fmt"
	"os"
	"path"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
	"golang.org/x/sync/errgroup"
)

type dirPerm struct {
	Pathname string
	Fileinfo objects.FileInfo
}

func (s *Sftp) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) (ret error) {
	defer close(results)
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(s.maxConcurrency)

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

			pathname := path.Join(s.Root(), record.Pathname)
			if record.FileInfo.Lmode.IsDir() {
				err := s.directory(record, pathname)
				results <- record.Error(err)

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
					err = s.symlink(record, pathname)
				} else if record.FileInfo.Lmode.IsRegular() {
					err = s.file(record, pathname)
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
		if err := s.permissions(dirPerms[i].Pathname, dirPerms[i].Fileinfo); err != nil {
			return err
		}
	}

	return ret
}

func (s *Sftp) directory(record *connectors.Record, pathname string) error {
	var err error
	if record.Pathname == "/" {
		// special case for the root directory of the restore,
		// we optionally create it, but only it, not the whole
		// structure up to it.
		err = s.client.Mkdir(pathname)
		if err != nil {
			dir, serr := s.client.Stat(pathname)
			if serr != nil {
				// nothing, leave err to the original value
			} else {
				if !dir.IsDir() {
					return fmt.Errorf("failed to mkdir %s: not a directory",
						pathname)
				}
				// it already exists, nothing to do.
				err = nil
			}
		}
	} else {
		err = s.client.Mkdir(pathname)
	}

	if err != nil {
		return fmt.Errorf("mkdir %s failed: %w", pathname, err)
	}
	return nil
}

func (s *Sftp) symlink(record *connectors.Record, pathname string) error {
	if err := s.client.Symlink(record.Target, pathname); err != nil {
		return fmt.Errorf("could not create symlink")
	}
	// don't attempt to p.setPerms in here, sftp lacks a lchown(2)
	// thingy.
	return nil
}

func (s *Sftp) hardlink(record *connectors.Record, pathname string) error {
	fileinfo := record.FileInfo
	key := fmt.Sprintf("%d:%d", fileinfo.Dev(), fileinfo.Ino())

	v, err, _ := s.hlCreate.Do(key, func() (any, error) {
		if v, ok := s.hlCanon.Load(key); ok {
			return v, nil
		}
		if err := s.writeAtomic(record, pathname); err != nil {
			return "", err
		}
		s.hlCanon.Store(key, pathname)
		return pathname, nil
	})
	if err != nil {
		return err
	}
	canonPath := v.(string)

	// If we are not the canonical path, create a hardlink
	if canonPath != pathname {
		if err := s.client.Link(canonPath, pathname); err != nil {
			return fmt.Errorf("could not create hardink %s -> %s", canonPath, pathname)
		}
	} else {
		if err := s.chown(canonPath, record.FileInfo); err != nil {
			return err
		}
	}

	return nil
}

func (s *Sftp) file(record *connectors.Record, pathname string) error {
	if record.FileInfo.Lnlink > 1 {
		return s.hardlink(record, pathname)
	}
	if err := s.writeAtomic(record, pathname); err != nil {
		return err
	}

	return s.chown(pathname, record.FileInfo)
}

func (s *Sftp) writeAtomic(record *connectors.Record, pathname string) error {
	_, err := writeFileAtomic(s.client, pathname, record.Reader)
	if err != nil {
		return err
	}

	fileinfo := record.FileInfo
	mode := fileinfo.Mode().Perm() | fileinfo.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)
	if err := s.client.Chmod(pathname, mode); err != nil {
		return fmt.Errorf("could not chmod")
	}
	return nil
}

func (s *Sftp) permissions(pathname string, fileinfo objects.FileInfo) error {
	if fileinfo.Mode()&os.ModeSymlink == 0 {
		// Preserve all permission bits including setuid (04000), setgid (02000), and sticky bit (01000)
		// Use the full mode which includes these special bits, not just Mode().Perm()
		mode := fileinfo.Mode().Perm() | fileinfo.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)
		if err := s.client.Chmod(pathname, mode); err != nil {
			return fmt.Errorf("could not chmod")
		}
	}
	return s.chown(pathname, fileinfo)
}

func (s *Sftp) chown(pathname string, fileinfo objects.FileInfo) error {
	if !s.setOwner {
		return nil
	}

	err := s.client.Chown(pathname, int(fileinfo.Luid), int(fileinfo.Lgid))
	if err != nil {
		return fmt.Errorf("failed to set owner/group on %s: %w",
			pathname, err)
	}
	return nil
}
