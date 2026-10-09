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
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/stretchr/testify/assert"
)

// Only the encryption keys are kept; everything else Glance returns is dropped.
func TestEncryptionImageProperties(t *testing.T) {
	tests := []struct {
		name string
		img  *images.Image
		want map[string]string
	}{
		{"nil image", nil, nil},
		{"no properties", &images.Image{}, nil},
		{"unrelated properties are dropped", &images.Image{Properties: map[string]any{"os_type": "linux"}}, nil},
		{
			"encryption properties are kept, as-is",
			&images.Image{Properties: map[string]any{
				"cinder_encryption_key_id":              "key-1",
				"cinder_encryption_key_deletion_policy": "on_image_deletion",
				"os_type":                               "linux",
			}},
			map[string]string{"cinder_encryption_key_id": "key-1", "cinder_encryption_key_deletion_policy": "on_image_deletion"},
		},
		{
			"a non-string or empty value is dropped",
			&images.Image{Properties: map[string]any{
				"cinder_encryption_key_id": "",
				"os_hidden":                false,
			}},
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, EncryptionImageProperties(tt.img))
		})
	}
}

func TestRestoreImageProperties(t *testing.T) {
	assert.Nil(t, RestoreImageProperties(nil))
	want := map[string]string{"cinder_encryption_key_id": "key-1"}
	assert.Equal(t, want, RestoreImageProperties(&DiskMetadata{ImageProperties: want}))
}
