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
	"encoding/hex"
	"fmt"
	"io"
	"path"

	plakarnfs "github.com/PlakarKorp/integrations/nfs/common"
	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/objects"
)

// Buckets stores content-addressed blobs (packfiles or states) under 256
// subdirectories keyed by the first byte of the MAC, matching the layout used
// by the fs and sftp storage backends.
type Buckets struct {
	conn *plakarnfs.Conn
	path string
}

func NewBuckets(conn *plakarnfs.Conn, path string) Buckets {
	return Buckets{conn: conn, path: path}
}

func (b *Buckets) Create() error {
	if err := mkdirAll(b.conn, b.path, 0o700); err != nil {
		return err
	}
	for i := range 256 {
		if err := b.conn.Mkdir(path.Join(b.path, fmt.Sprintf("%02x", i)), 0o700); err != nil {
			return fmt.Errorf("mkdir bucket %02x: %w", i, err)
		}
	}
	return nil
}

func (b *Buckets) bucketPath(mac objects.MAC) string {
	return path.Join(b.path, fmt.Sprintf("%02x", mac[0]), fmt.Sprintf("%064x", mac))
}

func (b *Buckets) List() ([]objects.MAC, error) {
	ret := make([]objects.MAC, 0)

	for i := range 256 {
		entries, err := b.conn.ReadDirPlus(path.Join(b.path, fmt.Sprintf("%02x", i)))
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.FileName == "." || entry.FileName == ".." || entry.IsDir() {
				continue
			}
			t, err := hex.DecodeString(entry.FileName)
			if err != nil || len(t) != 32 {
				continue
			}
			ret = append(ret, objects.MAC(t))
		}
	}

	return ret, nil
}

func (b *Buckets) Get(mac objects.MAC) (io.ReadCloser, error) {
	return b.conn.OpenReader(b.bucketPath(mac))
}

func (b *Buckets) GetRange(mac objects.MAC, rg *storage.Range) (io.ReadCloser, error) {
	return b.conn.OpenReaderRange(b.bucketPath(mac), int64(rg.Offset), int64(rg.Length))
}

func (b *Buckets) Remove(mac objects.MAC) error {
	return b.conn.Remove(b.bucketPath(mac))
}

func (b *Buckets) Put(mac objects.MAC, rd io.Reader) (int64, error) {
	return writeFileAtomic(b.conn, b.bucketPath(mac), b.path, 0o600, rd)
}
