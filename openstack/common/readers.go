/*
 * Copyright (c) 2026 Peeyush Aggarwal <peeyushaggarwal94@gmail.com>
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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/imagedata"
)

// ImageReader downloads the image, and runs cleanup once the download is
// closed.
func (c *Client) ImageReader(ctx context.Context, imageID string, cleanup *Cleanup) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		image, err := c.glance()
		if err != nil {
			return nil, err
		}
		rc, err := imagedata.Download(ctx, image, imageID).Extract()
		if err != nil {
			return nil, fmt.Errorf("download image %q: %w", imageID, err)
		}
		return &imageDownload{ReadCloser: rc, ctx: ctx, cleanup: cleanup}, nil
	}
}

type imageDownload struct {
	io.ReadCloser
	ctx     context.Context
	cleanup *Cleanup
}

func (r *imageDownload) Close() error {
	err := r.ReadCloser.Close()
	r.cleanup.Run(r.ctx)
	return err
}

// JSONReader marshals value when the reader is opened.
func JSONReader(value any) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal JSON: %w", err)
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	}
}
