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

package importer

import (
	"context"
	"os"
	"path"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
)

// walk traverses the share starting at the walk root, emitting one record per
// entry. Directory listings use Lstat semantics (symlinks are not followed), so
// reparse-point symlinks are captured as links rather than traversed.
func (imp *Importer) walk(ctx context.Context, records chan<- *connectors.Record) error {
	info, err := imp.conn.Lstat(imp.rootDir)
	if err != nil {
		records <- connectors.NewError(imp.rootDir, err)
		return nil
	}

	imp.emit(records, imp.rootDir, info)
	if info.IsDir() {
		imp.walkDir(ctx, records, imp.rootDir)
	}
	return nil
}

func (imp *Importer) walkDir(ctx context.Context, records chan<- *connectors.Record, dir string) {
	if ctx.Err() != nil {
		return
	}

	entries, err := imp.conn.ReadDir(dir)
	if err != nil {
		records <- connectors.NewError(dir, err)
		return
	}

	for _, info := range entries {
		if ctx.Err() != nil {
			return
		}

		name := info.Name()
		if name == "." || name == ".." {
			continue
		}

		full := path.Join(dir, name)
		if imp.excludes.IsExcluded(full, info.IsDir()) {
			continue
		}

		imp.emit(records, full, info)

		if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			imp.walkDir(ctx, records, full)
		}
	}
}

// emit converts an os.FileInfo into a connectors.Record and pushes it onto the
// channel, resolving symlink targets as needed.
func (imp *Importer) emit(records chan<- *connectors.Record, full string, info os.FileInfo) {
	fileinfo := objects.FileInfoFromStat(info)

	var target string
	if info.Mode()&os.ModeSymlink != 0 {
		if t, err := imp.conn.Readlink(full); err != nil {
			records <- connectors.NewError(full, err)
			return
		} else {
			target = t
		}
	}

	records <- connectors.NewRecord(full, target, fileinfo, []string{}, imp.open(full))
}
