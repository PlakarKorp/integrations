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
	"time"

	"github.com/PlakarKorp/integration-sdk"
	plakarnfs "github.com/PlakarKorp/integrations/nfs/common"
	"github.com/vmware/go-nfs-client/nfs"
)

// syntheticDirInfo describes the export root when the server returns no
// attributes for it (the LOOKUP of "/" has no final component to attribute).
func syntheticDirInfo(p string) integration.FileInfo {
	return integration.NewFileInfo(path.Base(p), 0, os.ModeDir|0o755, time.Unix(0, 0), 0, 0, 0, 0, 1)
}

// walk traverses the export starting at the walk root, emitting one record per
// entry. It uses NFSv3 READDIRPLUS, which returns names and attributes together,
// so no extra round-trip per entry is needed.
func (imp *Importer) walk(ctx context.Context, records chan<- *integration.Record) error {
	rootAttr, _, err := imp.conn.Lookup(imp.rootDir)
	if err != nil {
		records <- integration.NewError(imp.rootDir, err)
		return nil
	}

	if rootAttr == nil {
		// LOOKUP of the export root ("/") returns no attributes (there is no
		// final path component to look up). The root of an NFS export is
		// always a directory, so emit a synthetic directory record and
		// descend into it.
		records <- integration.NewRecord(imp.rootDir, "", syntheticDirInfo(imp.rootDir), []string{}, nil)
		imp.walkDir(ctx, records, imp.rootDir)
		return nil
	}

	imp.emit(records, imp.rootDir, path.Base(imp.rootDir), rootAttr)

	if plakarnfs.IsDir(rootAttr) {
		imp.walkDir(ctx, records, imp.rootDir)
	}
	return nil
}

func (imp *Importer) walkDir(ctx context.Context, records chan<- *integration.Record, dir string) {
	if ctx.Err() != nil {
		return
	}

	entries, err := imp.conn.ReadDirPlus(dir)
	if err != nil {
		records <- integration.NewError(dir, err)
		return
	}

	for _, entry := range entries {
		if ctx.Err() != nil {
			return
		}

		name := entry.Name()
		if name == "." || name == ".." {
			continue
		}
		if !entry.Attr.IsSet {
			// The server declined to return attributes for this entry; we
			// can't classify or read it reliably, so surface it as an error
			// rather than silently dropping it.
			records <- integration.NewError(path.Join(dir, name), errMissingAttr)
			continue
		}

		attr := &entry.Attr.Attr
		full := path.Join(dir, name)

		if imp.excludes.IsExcluded(full, plakarnfs.IsDir(attr)) {
			continue
		}

		imp.emit(records, full, name, attr)

		if plakarnfs.IsDir(attr) {
			imp.walkDir(ctx, records, full)
		}
	}
}

// emit converts an NFS attribute into an integration.Record and pushes it onto
// the channel, resolving symlink targets as needed.
func (imp *Importer) emit(records chan<- *integration.Record, full, name string, attr *nfs.Fattr) {
	fileinfo := plakarnfs.FileInfoFromAttr(name, attr)

	var target string
	if plakarnfs.IsSymlink(attr) {
		if t, err := imp.conn.Readlink(full); err != nil {
			records <- integration.NewError(full, err)
			return
		} else {
			target = t
		}
	}

	records <- integration.NewRecord(full, target, fileinfo, []string{}, imp.open(full))
}
