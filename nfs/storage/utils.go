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

package storage

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"

	plakarnfs "github.com/PlakarKorp/integrations/nfs/common"
)

// writeFileAtomic writes r to a temp file under tmpDir, then renames it to
// finalPath, so a reader never observes a partially written file. NFSv3
// RENAME is atomic (RFC 1813 §3.3.14) regardless of which directories the
// temp and final names live in, as long as they're on the same export.
func writeFileAtomic(conn *plakarnfs.Conn, finalPath, tmpDir string, perm os.FileMode, r io.Reader) (int64, error) {
	tmp, err := tempName(tmpDir)
	if err != nil {
		return 0, err
	}

	cr := &countingReader{r: r}
	if err := conn.WriteFile(tmp, perm, cr); err != nil {
		_ = conn.Remove(tmp)
		return 0, err
	}

	if err := conn.Rename(tmp, finalPath); err != nil {
		_ = conn.Remove(tmp)
		return 0, err
	}

	return cr.n, nil
}

func tempName(dir string) (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return path.Join(dir, ".tmp-"+hex.EncodeToString(buf[:])), nil
}

// countingReader tracks how many bytes have been read through it, so Put can
// report the number of bytes written without a second pass.
type countingReader struct {
	r io.Reader
	n int64
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	cr.n += int64(n)
	return n, err
}

// mkdirAll creates pathname and any missing parents, tolerating a directory
// that already exists (NFS MKDIR errors if the target is already there).
func mkdirAll(conn *plakarnfs.Conn, pathname string, perm os.FileMode) error {
	if pathname == "" || pathname == "/" {
		return nil
	}
	if isDir(conn, pathname) {
		return nil
	}
	if parent := path.Dir(pathname); parent != pathname {
		if err := mkdirAll(conn, parent, perm); err != nil {
			return err
		}
	}
	if err := conn.Mkdir(pathname, perm); err != nil {
		if isDir(conn, pathname) {
			return nil
		}
		return fmt.Errorf("mkdir %q: %w", pathname, err)
	}
	return nil
}

func isDir(conn *plakarnfs.Conn, pathname string) bool {
	attr, _, err := conn.Lookup(pathname)
	if err != nil {
		return false
	}
	if attr == nil {
		// The export root has no attributes of its own but is always a directory.
		return true
	}
	return plakarnfs.IsDir(attr)
}
