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

package importer

import (
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sync"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/pkg/sftp"
)

type file struct {
	path string
	info os.FileInfo
}

var (
	SkipDir = errors.New("skip this directory")
	SkipAll = errors.New("skip everything and stop the walk")
)

// Worker pool to handle file scanning in parallel
func (imp *Importer) walkDir_worker(jobs <-chan file, records chan<- *connectors.Record, wg *sync.WaitGroup) {
	defer wg.Done()

	for p := range jobs {
		// fixup the rootdir if it happened to be a file
		if !p.info.IsDir() && p.path == imp.Root() {
			imp.rootDir = filepath.Dir(imp.Root())
		}

		fileinfo := objects.FileInfoFromStat(p.info)
		//fileinfo.Lusername, fileinfo.Lgroupname = imp.lookupIDs(fileinfo.Uid(), fileinfo.Gid())

		var originFile string
		var err error
		if p.info.Mode()&os.ModeSymlink != 0 {
			originFile, err = imp.client.ReadLink(p.path)
			if err != nil {
				records <- connectors.NewError(p.path, err)
				continue
			}
		}

		entrypath := p.path

		records <- connectors.NewRecord(entrypath, originFile, fileinfo, []string{},
			func() (io.ReadCloser, error) {
				return imp.client.Open(p.path)
			})
	}
}

func (imp *Importer) walkDir_addPrefixDirectories(root string, records chan<- *connectors.Record) {
	for {
		var finfo objects.FileInfo

		sb, err := imp.client.Lstat(root)
		if err != nil {
			records <- connectors.NewError(root, err)
			finfo = objects.FileInfo{
				Lname: filepath.Base(root),
				Lmode: os.ModeDir | 0755,
			}
		} else {
			finfo = objects.FileInfoFromStat(sb)
		}

		records <- connectors.NewRecord(root, "", finfo, nil, nil)

		newroot := filepath.Dir(root)
		if newroot == root { // base case for "/" or "C:\"
			break
		}
		root = newroot
	}
}

func walkdir(client *sftp.Client, info os.FileInfo, p string, walkFn func(string, os.FileInfo, error) error) error {
	if err := walkFn(p, info, nil); err != nil {
		return err
	}

	if !info.IsDir() {
		return nil
	}

	entries, err := client.ReadDir(p)
	if err != nil {
		return walkFn(p, nil, err)
	}

	for _, entry := range entries {
		newPath := path.Join(p, entry.Name())
		if err := walkdir(client, entry, newPath, walkFn); err != nil {
			if err == SkipDir {
				continue
			}
			return err
		}
	}
	return nil
}

func SFTPWalk(client *sftp.Client, remotePath string, walkFn func(path string, info os.FileInfo, err error) error) error {
	info, err := client.Lstat(remotePath)
	if err != nil {
		err = walkFn(remotePath, nil, err)
		goto done
	}

	err = walkdir(client, info, remotePath, walkFn)
done:
	if err == SkipDir || err == SkipAll {
		err = nil
	}
	return err
}
