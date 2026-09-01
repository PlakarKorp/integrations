/*
 * Copyright (c) 2025 Eric Faurot <eric@faurot.net>
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
	"crypto/rand"
	"fmt"
	"io"
	"os"

	"github.com/pkg/sftp"
)

func writeFileAtomic(sftpClient *sftp.Client, pathname string, rd io.Reader) (int64, error) {
	tmpName := fmt.Sprintf("%s.tmp.%s", pathname, rand.Text())

	tmp, err := sftpClient.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return 0, fmt.Errorf("could not create temporary file %s: %w", tmpName, err)
	}

	ok := false
	defer func() {
		if !ok {
			sftpClient.Remove(tmpName)
		}
	}()

	var nbytes int64
	if nbytes, err = tmp.ReadFromWithConcurrency(rd, 0); err != nil {
		tmp.Close()
		return 0, fmt.Errorf("failed to write %s: %w", pathname, err)
	}

	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("could not close %s: %w", pathname, err)
	}

	if err := sftpClient.Rename(tmpName, pathname); err != nil {
		return 0, fmt.Errorf("could not create %s: %w", pathname, err)
	}

	ok = true

	return nbytes, nil
}
