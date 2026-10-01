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

package block

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVolumeID(t *testing.T) {
	tests := []struct {
		path string
		id   string // empty when path is not a disk file
	}{
		{path: "/" + DiskName("384ba87e-a23e-4965-b3be-8f180e8a8625"), id: "384ba87e-a23e-4965-b3be-8f180e8a8625"},
		{path: "/vol_1.qcow2", id: "vol_1"},
		{path: "/" + MetadataName},
		{path: "/.qcow2"},
		{path: "vol-1.qcow2"},
		{path: "/dir/vol-1.qcow2"},
		{path: "/../vol-1.qcow2"},
		{path: "/vol-1.raw"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			id, ok := VolumeID(tt.path)
			assert.Equal(t, tt.id != "", ok)
			assert.Equal(t, tt.id, id)
		})
	}
}
