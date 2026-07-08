/*
 * Copyright (c) 2026 Antoine Dheygers <antoine.dheygers@cryptoweb.fr>
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
	"archive/tar"
	"fmt"
	"io/fs"
	"strings"

	"github.com/PlakarKorp/kloset/connectors"
)

// tarHeader rebuilds the tar entry for a record, inverse of the
// importer mapping.
func tarHeader(rec *connectors.Record) (*tar.Header, error) {
	name := strings.TrimPrefix(rec.Pathname, "/")
	if name == "" {
		return nil, fmt.Errorf("empty pathname")
	}
	fi := rec.FileInfo

	hdr := &tar.Header{
		Name:    name,
		Mode:    int64(fi.Lmode) & 0o7777,
		Uid:     int(fi.Luid),
		Gid:     int(fi.Lgid),
		ModTime: fi.LmodTime,
	}

	switch {
	case fi.Lmode.IsDir():
		hdr.Typeflag = tar.TypeDir
		hdr.Name += "/"
	case fi.Lmode&fs.ModeSymlink != 0:
		hdr.Typeflag = tar.TypeSymlink
		hdr.Linkname = rec.Target
	case fi.Lmode&fs.ModeCharDevice != 0:
		hdr.Typeflag = tar.TypeChar
	case fi.Lmode&fs.ModeDevice != 0:
		hdr.Typeflag = tar.TypeBlock
	case fi.Lmode&fs.ModeNamedPipe != 0:
		hdr.Typeflag = tar.TypeFifo
	default:
		hdr.Typeflag = tar.TypeReg
		hdr.Size = fi.Lsize
	}

	return hdr, nil
}
