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
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/snapshots"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
)

// DiskMetadata is the .METADATA.json next to a disk. Image.DiskFormat is the
// disk's real format, whatever its file is named. ImageProperties holds the
// image's restorable properties separately: gophercloud's images.Image
// decodes Properties from the wire but never encodes it back, so a round
// trip through JSON silently drops it from Image itself.
type DiskMetadata struct {
	Origin          DiskOrigin          `json:"origin"`
	Image           *images.Image       `json:"image,omitempty"`
	ImageProperties map[string]string   `json:"image_properties,omitempty"`
	Volume          *volumes.Volume     `json:"volume,omitempty"`
	Snapshot        *snapshots.Snapshot `json:"snapshot,omitempty"`
}

type DiskOrigin string

const (
	DiskOriginCinder DiskOrigin = "cinder"
	DiskOriginGlance DiskOrigin = "glance"
)

// encryptionImageProperties are the Glance properties Cinder reads to find
// the Barbican key an encrypted volume's data is under. A restore image
// needs them, or Cinder treats the already-encrypted bytes as plain data and
// wraps them in a new key instead of reusing the one they are locked with.
var encryptionImageProperties = []string{"cinder_encryption_key_id", "cinder_encryption_key_deletion_policy"}

// EncryptionImageProperties extracts the properties of img a restore must
// carry over, for DiskMetadata.ImageProperties, or nil if img is nil or has
// none of them set.
func EncryptionImageProperties(img *images.Image) map[string]string {
	if img == nil {
		return nil
	}
	var props map[string]string
	for _, key := range encryptionImageProperties {
		v, ok := img.Properties[key].(string)
		if !ok || v == "" {
			continue
		}
		if props == nil {
			props = make(map[string]string, len(encryptionImageProperties))
		}
		props[key] = v
	}
	return props
}

// RestoreImageProperties returns the image properties a restore must set on
// its own upload image, or nil if metadata is unset.
func RestoreImageProperties(metadata *DiskMetadata) map[string]string {
	if metadata == nil {
		return nil
	}
	return metadata.ImageProperties
}
