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

package storage

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"strings"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integrations/fs/reading"
)

type Buckets struct {
	path string
}

func NewBuckets(path string) Buckets {
	return Buckets{
		path: path,
	}
}

func (buckets *Buckets) Create() error {
	for i := range 256 {
		err := os.MkdirAll(filepath.Join(buckets.path, fmt.Sprintf("%02x", i)), 0700)
		if err != nil {
			return err
		}
	}

	return nil
}

func (buckets *Buckets) List() iter.Seq2[*integration.StorageResourceEntry, error] {
	return func(yield func(*integration.StorageResourceEntry, error) bool) {
		for bucket, err := range readdir(buckets.path) {
			if err != nil {
				yield(&integration.StorageResourceEntry{}, err)
				return
			}

			if !bucket.IsDir() {
				continue
			}
			for entry, err := range readdir(filepath.Join(buckets.path, bucket.Name())) {
				if err != nil {
					yield(&integration.StorageResourceEntry{}, err)
					return
				}
				if !entry.Type().IsRegular() {
					continue
				}
				t, err := hex.DecodeString(entry.Name())
				if err != nil {
					// XXX ignore the gibberish?
					continue
				}
				if len(t) != 32 {
					continue
				}
				info, err := entry.Info()
				if errors.Is(err, fs.ErrNotExist) {
					// removed since ReadDir
					continue
				}
				if err != nil {
					yield(&integration.StorageResourceEntry{}, err)
					return
				}
				e := integration.StorageResourceEntry{
					MAC:   integration.MAC(t),
					Size:  info.Size(),
					MTime: info.ModTime(),
				}
				if !yield(&e, nil) {
					return
				}
			}
		}
	}
}

func (buckets *Buckets) Path(mac integration.MAC) (string, error) {
	dest := filepath.Join(buckets.path,
		fmt.Sprintf("%02x", mac[0]),
		fmt.Sprintf("%064x", mac))

	/*
	 * This is so dumb.  But CodeQL is even more dumb, and can't
	 * realize that a MAC serialized in hex form and used as path
	 * can't escape, so it thinks there's an uncontrolled data
	 * used in path expression.
	 */
	if !strings.HasPrefix(dest, buckets.path) {
		return "", errors.New("invalid path")
	}
	return dest, nil
}

func (buckets *Buckets) Get(mac integration.MAC) (io.ReadCloser, error) {
	p, err := buckets.Path(mac)
	if err != nil {
		return nil, err
	}

	fp, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	return fp, nil
}

func (buckets *Buckets) GetBlob(mac integration.MAC, offset uint64, length uint32) (io.ReadCloser, error) {
	p, err := buckets.Path(mac)
	if err != nil {
		return nil, err
	}

	fp, err := os.Open(p)
	if err != nil {
		return nil, err
	}

	return reading.NewSectionReadCloser(fp, int64(offset), int64(length)), nil
}

func (buckets *Buckets) Remove(mac integration.MAC) error {
	p, err := buckets.Path(mac)
	if err != nil {
		return err
	}

	return os.Remove(p)
}

func (buckets *Buckets) Put(mac integration.MAC, rd io.Reader) (int64, error) {
	p, err := buckets.Path(mac)
	if err != nil {
		return 0, err
	}

	return WriteToFileAtomicTempDir(p, rd, buckets.path)
}
