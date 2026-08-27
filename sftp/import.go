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
	"errors"
	"io"
	"os"
	"path"
	"strings"
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
	skipDir = errors.New("skip this directory")
	skipAll = errors.New("skip everything and stop the walk")
)

func (s *Sftp) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)
	return s.walkDir_walker(ctx, records, s.maxConcurrency)
}

// Worker pool to handle file scanning in parallel
func (s *Sftp) walkDir_worker(jobs <-chan file, records chan<- *connectors.Record, wg *sync.WaitGroup) {
	defer wg.Done()

	for p := range jobs {
		// fixup the rootdir if it happened to be a file
		if !p.info.IsDir() && p.path == s.Root() {
			s.rootDir = path.Dir(s.Root())
		}

		var uid, gid uint64

		if sb, ok := p.info.Sys().(*sftp.FileStat); ok {
			uid = uint64(sb.UID)
			gid = uint64(sb.GID)
		}

		fileinfo := objects.FileInfo{
			Lname:    p.info.Name(),
			Lsize:    p.info.Size(),
			Lmode:    p.info.Mode(),
			LmodTime: p.info.ModTime(),
			Luid:     uid,
			Lgid:     gid,
		}

		var originFile string
		var err error
		if p.info.Mode()&os.ModeSymlink != 0 {
			originFile, err = s.client.ReadLink(p.path)
			if err != nil {
				records <- connectors.NewError(p.path, err)
				continue
			}
		}

		entrypath := p.path

		records <- connectors.NewRecord(entrypath, originFile, fileinfo, []string{},
			func() (io.ReadCloser, error) {
				return s.client.Open(p.path)
			})
	}
}

func (s *Sftp) walkDir_walker(ctx context.Context, records chan<- *connectors.Record, numWorkers int) error {
	jobs := make(chan file, numWorkers*4) // Buffered channel to feed paths to workers
	var wg sync.WaitGroup
	for range numWorkers {
		wg.Add(1)
		go s.walkDir_worker(jobs, records, &wg)
	}

	err := walk(s.client, s.rootDir, func(path string, info os.FileInfo, err error) error {
		if ctx.Err() != nil {
			return err
		}

		if err != nil {
			records <- connectors.NewError(path, err)
			return nil
		}

		if path != "/" {
			if s.excludes.IsExcluded(path, info.IsDir()) {
				return skipDir
			}
		}

		jobs <- file{path: path, info: info}
		return nil
	})

	close(jobs)
	wg.Wait()
	return err
}

func walkdir(client *sftp.Client, info os.FileInfo, mode os.FileMode, p string, walkFn func(string, os.FileInfo, error) error) error {
	if err := walkFn(p, info, nil); err != nil {
		return err
	}

	if mode&os.ModeDir == 0 {
		return nil
	}

	entries, err := client.ReadDir(p)
	if err != nil {
		return walkFn(p, nil, err)
	}

	for _, entry := range entries {
		newPath := path.Join(p, entry.Name())
		if err := walkdir(client, entry, entry.Mode(), newPath, walkFn); err != nil {
			if err == skipDir {
				continue
			}
			return err
		}
	}
	return nil
}

func walk(client *sftp.Client, remotePath string, walkFn func(path string, info os.FileInfo, err error) error) error {
	var mode os.FileMode

	info, err := client.Lstat(remotePath)
	if err != nil {
		err = walkFn(remotePath, nil, err)
		goto done
	}

	mode = info.Mode()
	if mode&os.ModeSymlink != 0 {
		info2, err := client.Stat(remotePath)
		if err == nil && info2.Mode()&os.ModeDir != 0 {
			resolved, err := client.ReadLink(remotePath)
			if err == nil &&
				strings.HasPrefix(resolved, "///?/GLOBALROOT/") {
				/*
				 * This should be a Windows Directory Symlink
				 * since Lstat() reports a symlink, Stat()
				 * reports a directory, and the resolved path
				 * begins like a Windows Volume ID.
				 * In regards to filesystem traversal we can
				 * treat this path as a directory.
				 */
				mode &= ^os.ModeSymlink
				mode |= os.ModeDir
			}
		}
	}

	err = walkdir(client, info, mode, remotePath, walkFn)
done:
	if err == skipDir || err == skipAll {
		err = nil
	}
	return err
}
