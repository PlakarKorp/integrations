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
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"iter"
	"path"
	"sync"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integrations/sftp/reading"
	"github.com/pkg/sftp"
	"golang.org/x/sync/errgroup"
)

type buckets struct {
	client *sftp.Client
	path   string
}

func newBuckets(sftpClient *sftp.Client, path string) buckets {
	return buckets{
		client: sftpClient,
		path:   path,
	}
}

func (buckets *buckets) Create() error {
	var g errgroup.Group

	for i := range 256 {
		g.Go(func() error {
			dir := path.Join(buckets.path, fmt.Sprintf("%02x", i))
			if err := buckets.client.MkdirAll(dir); err != nil {
				return err
			}
			if err := buckets.client.Chmod(dir, 0700); err != nil {
				return err
			}
			return nil
		})
	}

	return g.Wait()
}

func (buckets *buckets) List(ctx context.Context) iter.Seq[integration.MAC] {
	return func(yield func(integration.MAC) bool) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		macs := make(chan integration.MAC)
		var wg sync.WaitGroup
		for i := range 256 {
			path := path.Join(buckets.path, fmt.Sprintf("%02x", i))
			wg.Add(1)
			go func(path string) {
				defer wg.Done()
				for _, mac := range buckets.readBucket(path) {
					select {
					case macs <- mac:
					case <-ctx.Done():
						return
					}
				}
			}(path)
		}
		go func() {
			wg.Wait()
			close(macs)
		}()

		for mac := range macs {
			if !yield(mac) {
				return
			}
		}
	}
}

// readBucket ReaDir, then map to MACs so FileInfos can be freed ASAP.
func (buckets *buckets) readBucket(path string) []integration.MAC {
	entries, err := buckets.client.ReadDir(path)
	if err != nil {
		return nil
	}

	macs := make([]integration.MAC, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == "." || entry.Name() == ".." {
			continue
		}
		if entry.IsDir() {
			continue
		}
		t, err := hex.DecodeString(entry.Name())
		if err != nil {
			continue
		}
		if len(t) != 32 {
			continue
		}
		macs = append(macs, integration.MAC(t))
	}
	return macs
}

func (buckets *buckets) Path(mac integration.MAC) string {
	return path.Join(buckets.path,
		fmt.Sprintf("%02x", mac[0]),
		fmt.Sprintf("%064x", mac))
}

func (buckets *buckets) Get(mac integration.MAC, rg *integration.Range) (io.ReadCloser, error) {
	fp, err := buckets.client.Open(buckets.Path(mac))
	if err != nil {
		return nil, err
	}

	if rg == nil {
		return fp, nil
	}

	return reading.NewSectionReadCloser(fp, int64(rg.Offset), int64(rg.Length)), nil
}

func (buckets *buckets) Remove(mac integration.MAC) error {
	return buckets.client.Remove(buckets.Path(mac))
}

func (buckets *buckets) Put(mac integration.MAC, rd io.Reader) (int64, error) {
	return writeFileAtomic(buckets.client, buckets.Path(mac), rd, nil)
}
