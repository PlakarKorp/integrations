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
	"io"
	"os"
	"path"

	plakarsmb "github.com/PlakarKorp/integrations/smb/common"
	"github.com/PlakarKorp/kloset/objects"
)

// writeFileAtomic writes r to a temporary file next to finalPath, then renames
// it into place, so a reader never observes a partially written file.
//
// SMB2 rename does not replace an existing target. For content-addressed
// objects (packfiles, states) and uniquely named locks, an existing target
// holds the same bytes, so callers pass existsOK to drop the temporary file
// and report success. CONFIG passes false: an existing one means another
// client created a repository there concurrently, and claiming success would
// leave us writing data under a configuration that isn't ours.
func writeFileAtomic(conn *plakarsmb.Conn, finalPath string, r io.Reader, existsOK bool) (int64, error) {
	tmp, err := tempName(path.Dir(finalPath))
	if err != nil {
		return 0, err
	}

	fp, err := conn.Create(tmp)
	if err != nil {
		return 0, err
	}

	n, err := io.Copy(fp, r)
	if err != nil {
		_ = fp.Close()
		_ = conn.Remove(tmp)
		return 0, err
	}
	if err := fp.Close(); err != nil {
		_ = conn.Remove(tmp)
		return 0, err
	}

	if err := conn.Rename(tmp, finalPath); err != nil {
		_ = conn.Remove(tmp)
		if _, serr := conn.Lstat(finalPath); existsOK && serr == nil {
			return n, nil
		}
		return 0, err
	}

	return n, nil
}

func tempName(dir string) (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return path.Join(dir, ".tmp-"+hex.EncodeToString(buf[:])), nil
}

// macsFromEntries keeps the regular files whose name is a hex-encoded MAC,
// skipping directories and leftover temporary files.
func macsFromEntries(entries []os.FileInfo) []objects.MAC {
	ret := make([]objects.MAC, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		t, err := hex.DecodeString(entry.Name())
		if err != nil || len(t) != len(objects.MAC{}) {
			continue
		}
		ret = append(ret, objects.MAC(t))
	}
	return ret
}
