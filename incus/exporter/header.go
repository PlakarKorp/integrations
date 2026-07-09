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

	// Rebuild the raw tar mode from the portable fs.FileMode flags:
	// permission bits verbatim, plus setuid/setgid/sticky translated
	// back to their 0o4000/0o2000/0o1000 tar bits.
	mode := int64(fi.Lmode & fs.ModePerm)
	if fi.Lmode&fs.ModeSetuid != 0 {
		mode |= 0o4000
	}
	if fi.Lmode&fs.ModeSetgid != 0 {
		mode |= 0o2000
	}
	if fi.Lmode&fs.ModeSticky != 0 {
		mode |= 0o1000
	}
	// Legacy compat: snapshots taken before this fix stored the raw
	// tar special bits directly in Lmode instead of the Go
	// fs.ModeSetuid/Setgid/Sticky flags. OR them back in here too -
	// harmless overlap when both forms are present, but required so
	// exporting a pre-fix snapshot doesn't silently drop
	// setuid/setgid/sticky.
	mode |= int64(fi.Lmode) & 0o7000

	hdr := &tar.Header{
		Name:    name,
		Mode:    mode,
		Uid:     int(fi.Luid),
		Gid:     int(fi.Lgid),
		ModTime: fi.LmodTime,
	}

	switch {
	case fi.Lmode.IsDir():
		hdr.Typeflag = tar.TypeDir
		hdr.Name += "/"
	case fi.Lmode&fs.ModeSymlink != 0:
		if fi.Lnlink > 1 {
			// Hardlink (see importer/finfo.go's linkTarget): Target
			// was recorded tar-root-relative, matching how entry
			// names are stored, so re-emit it the same way here.
			hdr.Typeflag = tar.TypeLink
			hdr.Linkname = strings.TrimPrefix(rec.Target, "/")
		} else {
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = rec.Target
		}
	case fi.Lmode&fs.ModeCharDevice != 0:
		hdr.Typeflag = tar.TypeChar
		// Reverse of the Ldev encoding documented in
		// importer/finfo.go: devmajor in the high 32 bits, devminor
		// in the low 32 bits.
		hdr.Devmajor = int64(fi.Ldev >> 32)
		hdr.Devminor = int64(fi.Ldev & 0xffffffff)
	case fi.Lmode&fs.ModeDevice != 0:
		hdr.Typeflag = tar.TypeBlock
		// Same Ldev encoding as tar.TypeChar above.
		hdr.Devmajor = int64(fi.Ldev >> 32)
		hdr.Devminor = int64(fi.Ldev & 0xffffffff)
	case fi.Lmode&fs.ModeNamedPipe != 0:
		hdr.Typeflag = tar.TypeFifo
	default:
		hdr.Typeflag = tar.TypeReg
		hdr.Size = fi.Lsize
	}

	return hdr, nil
}
