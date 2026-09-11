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
	"strings"

	"github.com/PlakarKorp/kloset/objects"
)

// finfo maps a tar header to a kloset FileInfo (same approach as the
// docker integration).
func finfo(hdr *tar.Header) objects.FileInfo {
	// Raw tar mode bits carry setuid/setgid/sticky (0o4000/0o2000/0o1000)
	// alongside the permission bits. Convert them to Go's abstract
	// fs.ModeSetuid/Setgid/Sticky flags (the same conversion
	// hdr.FileInfo().Mode() performs) so they survive round-tripping
	// through connectors other than incus<->incus, which only
	// understand fs.FileMode's portable flag bits, not raw tar bits.
	mode := fs.FileMode(hdr.Mode) & fs.ModePerm
	if hdr.Mode&0o4000 != 0 {
		mode |= fs.ModeSetuid
	}
	if hdr.Mode&0o2000 != 0 {
		mode |= fs.ModeSetgid
	}
	if hdr.Mode&0o1000 != 0 {
		mode |= fs.ModeSticky
	}

	f := objects.FileInfo{
		Lname:    path.Base(path.Clean(hdr.Name)),
		Lsize:    hdr.Size,
		Lmode:    mode,
		LmodTime: hdr.ModTime,
		Luid:     uint64(hdr.Uid),
		Lgid:     uint64(hdr.Gid),
		Lnlink:   1,
	}

	switch hdr.Typeflag {
	case tar.TypeSymlink:
		f.Lmode |= fs.ModeSymlink
	case tar.TypeLink:
		// hardlinks are mapped to symlinks: kloset has no hardlink
		// notion. Lnlink>1 flags the entry as a hardlink so the
		// exporter can tell it apart from a plain symlink and
		// re-emit tar.TypeLink instead of TypeSymlink (see
		// linkTarget below for how Target is normalized).
		f.Lmode |= fs.ModeSymlink
		f.Lnlink = 2
	case tar.TypeChar:
		f.Lmode |= fs.ModeCharDevice
		// objects.FileInfo has no dedicated rdev field: pack
		// devmajor into the high 32 bits and devminor into the low
		// 32 bits of Ldev. The exporter reverses this encoding.
		f.Ldev = uint64(hdr.Devmajor)<<32 | uint64(hdr.Devminor)&0xffffffff
	case tar.TypeBlock:
		f.Lmode |= fs.ModeDevice
		// Same Ldev encoding as tar.TypeChar above.
		f.Ldev = uint64(hdr.Devmajor)<<32 | uint64(hdr.Devminor)&0xffffffff
	case tar.TypeDir:
		f.Lmode |= fs.ModeDir
	case tar.TypeFifo:
		f.Lmode |= fs.ModeNamedPipe
	}

	return f
}

// linkTarget returns the target to record for a symlink/hardlink tar
// entry. Plain symlinks keep hdr.Linkname verbatim: it is resolved
// relative to the entry's own directory at restore time. Hardlinks
// (tar.TypeLink) encode Linkname relative to the tar root instead
// (e.g. "backup/container/rootfs/bin/busybox"): recorded verbatim, a
// generic exporter (e.g. kloset's fs exporter) would materialize a
// symlink whose target never resolves. So the target is rewritten as
// the RELATIVE path from the link's parent directory to the linked
// file, computed in tar-name space. Any restore destination then
// produces a relative symlink that resolves correctly inside the
// restored tree, and the incus exporter can rebuild the
// tar-root-relative Linkname by resolving Target against the record's
// own directory.
func linkTarget(hdr *tar.Header) string {
	if hdr.Typeflag == tar.TypeLink {
		name := path.Join("/", hdr.Name)
		link := path.Join("/", hdr.Linkname)
		return relPath(path.Dir(name), link)
	}
	return hdr.Linkname
}

// relPath computes the relative slash-path from directory fromDir to
// the path to. Both arguments are absolute slash-paths in tar-name
// space; this is deliberately pure string/slash logic, never OS
// filepath semantics.
func relPath(fromDir, to string) string {
	from := splitPath(fromDir)
	target := splitPath(to)
	common := 0
	for common < len(from) && common < len(target) && from[common] == target[common] {
		common++
	}
	parts := make([]string, 0, len(from)-common+len(target)-common)
	for range from[common:] {
		parts = append(parts, "..")
	}
	parts = append(parts, target[common:]...)
	return strings.Join(parts, "/")
}

// splitPath cleans an absolute slash-path and splits it into its
// components; the root "/" yields nil.
func splitPath(p string) []string {
	p = strings.TrimPrefix(path.Clean(p), "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// recordPath normalizes a tar entry name into an absolute record path.
func recordPath(hdr *tar.Header) string {
	return path.Join("/", hdr.Name)
}
