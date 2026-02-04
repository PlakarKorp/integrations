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
	"context"
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

// Worker pool to handle file scanning in parallel
func (imp *Importer) walkDir_worker(ctx context.Context, jobs <-chan file, records chan<- *connectors.Record, wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		var (
			p  file
			ok bool
		)

		select {
		case p, ok = <-jobs:
			if !ok {
				return
			}
		}

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

func SFTPWalk(client *sftp.Client, remotePath string, walkFn func(path string, info os.FileInfo, err error) error) error {
	info, err := client.Lstat(remotePath)
	if err != nil {
		// If we can't stat the file, call walkFn with the error.
		return walkFn(remotePath, nil, err)
	}
	// Call the walk function for the current file/directory.
	if err := walkFn(remotePath, info, nil); err != nil {
		return err
	}

	// If it's not a directory, nothing more to do.
	if !info.IsDir() {
		return nil
	}
	// List the directory contents.
	entries, err := client.ReadDir(remotePath)
	if err != nil {
		return walkFn(remotePath, info, err)
	}
	// Recursively walk each entry.
	for _, entry := range entries {
		newPath := path.Join(remotePath, entry.Name()) // Use "path" since remote paths are POSIX style.
		if err := SFTPWalk(client, newPath, walkFn); err != nil {
			return err
		}
	}
	return nil
}
