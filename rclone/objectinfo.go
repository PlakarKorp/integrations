/*
 * Copyright (c) 2026 Omar Polo <op@omarpolo.com>
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

package rclone

import (
	"context"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	rclonefs "github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/hash"
)

// objectinfo implements rclonefs.ObjectInfo
type objectinfo struct {
	record *connectors.Record
}

var _ rclonefs.ObjectInfo = &objectinfo{}

func (o *objectinfo) Fs() rclonefs.Info {
	return nil
}

func (o *objectinfo) String() string { return o.record.Pathname }
func (o *objectinfo) Remote() string { return o.record.Pathname }
func (o *objectinfo) Size() int64    { return o.record.FileInfo.Lsize }
func (o *objectinfo) Storable() bool { return true }

func (o *objectinfo) ModTime(context.Context) time.Time {
	return o.record.FileInfo.LmodTime
}

func (o *objectinfo) Hash(context.Context, hash.Type) (string, error) {
	return "", hash.ErrUnsupported
}
