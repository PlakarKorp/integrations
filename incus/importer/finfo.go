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

package importer

import (
	"archive/tar"
	"io/fs"
	"path"

	"github.com/PlakarKorp/kloset/objects"
)

// finfo maps a tar header to a kloset FileInfo (same approach as the
// docker integration).
func finfo(hdr *tar.Header) objects.FileInfo {
	f := objects.FileInfo{
		Lname:    path.Base(path.Clean(hdr.Name)),
		Lsize:    hdr.Size,
		Lmode:    fs.FileMode(hdr.Mode),
		LmodTime: hdr.ModTime,
		Luid:     uint64(hdr.Uid),
		Lgid:     uint64(hdr.Gid),
		Lnlink:   1,
	}

	switch hdr.Typeflag {
	case tar.TypeSymlink, tar.TypeLink:
		// hardlinks are mapped to symlinks: kloset has no hardlink
		// notion; acceptable for container rootfs (v1 caveat).
		f.Lmode |= fs.ModeSymlink
	case tar.TypeChar:
		f.Lmode |= fs.ModeCharDevice
	case tar.TypeBlock:
		f.Lmode |= fs.ModeDevice
	case tar.TypeDir:
		f.Lmode |= fs.ModeDir
	case tar.TypeFifo:
		f.Lmode |= fs.ModeNamedPipe
	}

	return f
}

// recordPath normalizes a tar entry name into an absolute record path.
func recordPath(hdr *tar.Header) string {
	return path.Join("/", hdr.Name)
}
