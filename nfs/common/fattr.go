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

package common

import (
	"os"
	"time"

	"github.com/PlakarKorp/kloset/objects"
	"github.com/vmware/go-nfs-client/nfs"
)

// NFSv3 file types (RFC 1813, ftype3). go-nfs-client exposes these as Fattr.Type.
const (
	nf3Reg  = 1
	nf3Dir  = 2
	nf3Blk  = 3
	nf3Chr  = 4
	nf3Lnk  = 5
	nf3Sock = 6
	nf3FIFO = 7
)

// POSIX permission/special bits as they appear in the NFS wire mode (Fattr.FileMode).
const (
	wireSetuid = 0o4000
	wireSetgid = 0o2000
	wireSticky = 0o1000
	wirePerm   = 0o0777
)

// fattrMode reconstructs a Go os.FileMode from an NFSv3 Fattr.
//
// This is necessary because go-nfs-client's Fattr.Mode() returns the raw wire
// mode (e.g. 0100644) cast straight to os.FileMode, which collides with Go's
// own ModeDir/ModeSymlink bit layout. We instead build the mode from the
// explicit NFS file type plus the permission and special bits.
func fattrMode(attr *nfs.Fattr) os.FileMode {
	mode := os.FileMode(attr.FileMode & wirePerm)

	if attr.FileMode&wireSetuid != 0 {
		mode |= os.ModeSetuid
	}
	if attr.FileMode&wireSetgid != 0 {
		mode |= os.ModeSetgid
	}
	if attr.FileMode&wireSticky != 0 {
		mode |= os.ModeSticky
	}

	switch attr.Type {
	case nf3Dir:
		mode |= os.ModeDir
	case nf3Lnk:
		mode |= os.ModeSymlink
	case nf3Blk:
		mode |= os.ModeDevice
	case nf3Chr:
		mode |= os.ModeDevice | os.ModeCharDevice
	case nf3Sock:
		mode |= os.ModeSocket
	case nf3FIFO:
		mode |= os.ModeNamedPipe
	case nf3Reg:
		// regular file: no type bits
	}

	return mode
}

// FileInfoFromAttr maps an NFSv3 Fattr into a kloset objects.FileInfo.
//
// dev is the export-wide device id (Fattr.FSID); ino, uid, gid and nlink come
// straight off the wire so Plakar can dedup, attribute ownership and detect
// hardlinks.
func FileInfoFromAttr(name string, attr *nfs.Fattr) objects.FileInfo {
	modTime := time.Unix(int64(attr.Mtime.Seconds), int64(attr.Mtime.Nseconds))
	return objects.NewFileInfo(
		name,
		int64(attr.Filesize),
		fattrMode(attr),
		modTime,
		attr.FSID,
		attr.Fileid,
		uint64(attr.UID),
		uint64(attr.GID),
		uint16(attr.Nlink),
	)
}

// IsSymlink reports whether attr describes a symbolic link.
func IsSymlink(attr *nfs.Fattr) bool { return attr.Type == nf3Lnk }

// IsDir reports whether attr describes a directory.
func IsDir(attr *nfs.Fattr) bool { return attr.Type == nf3Dir }

// IsRegular reports whether attr describes a regular file.
func IsRegular(attr *nfs.Fattr) bool { return attr.Type == nf3Reg }
