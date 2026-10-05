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
	"regexp"
	"strings"
)

// The snapshot layout the importer writes and the exporter reads.

const Protocol = "openstack-block"

// MetadataName is the file holding the disk's common.DiskMetadata.
const MetadataName = ".METADATA.json"

const diskExt = ".qcow2"

// The volume ID names the disk file, so it must be a single path segment.
//
//	^              start
//	[0-9A-Za-z_-]+ one or more of: digit, letter, underscore, dash
//	$              end
var VolumeIDFormat = regexp.MustCompile(`^[0-9A-Za-z_-]+$`)

// DiskName is <volume-id>.qcow2 whatever the disk's format: .METADATA.json
// records the real one.
func DiskName(volumeID string) string {
	return volumeID + diskExt
}

// VolumeID returns the ID in a /<volume-id>.qcow2 path.
func VolumeID(path string) (string, bool) {
	name, ok := strings.CutPrefix(path, "/")
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(name, diskExt)
	if !ok || !VolumeIDFormat.MatchString(id) {
		return "", false
	}
	return id, true
}
