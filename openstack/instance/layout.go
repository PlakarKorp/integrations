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

package instance

import "github.com/PlakarKorp/integrations/openstack/block"

// The instance snapshot layout, like the block layout: /.METADATA.json
// (common.ServerMetadata), then a /block-storage/<origin>-<id>/ directory
// per disk (the root image and each attached volume), each with its own
// disk.qcow2 and common.DiskMetadata. Kloset walks a snapshot in path order,
// so every .METADATA.json reaches the exporter before its disk.

const Protocol = "openstack-instance"

// DiskName names every disk, whatever its format, as the block layout does.
const DiskName = "disk.qcow2"

// MetadataName is shared with the block layout.
const MetadataName = block.MetadataName

// RootDir is the root disk's directory: the Glance image Import snapshots it
// into.
func RootDir(imageID string) string {
	return "/block-storage/glance-" + imageID
}

// VolumeDir is one attached volume's directory, by its Cinder ID.
func VolumeDir(volumeID string) string {
	return "/block-storage/cinder-" + volumeID
}
