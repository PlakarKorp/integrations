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
	"fmt"
	"regexp"
	"strings"

	"github.com/PlakarKorp/integrations/openstack/common"
)

// The snapshot layout the importer writes and the exporter reads.

const Protocol = "openstack-block"

// MetadataName is the file holding the disk's common.DiskMetadata.
const MetadataName = ".METADATA.json"

// diskPattern matches <id>.<ext> for the formats a disk is written in, and
// captures the ID.
var diskPattern = regexp.MustCompile(`^([^/]+)\.(qcow2|raw)$`)

// DiskName is <volume-id>.<ext> whatever the disk's format: .METADATA.json
// records the real one.
func DiskName(volumeID string, diskFormat string) string {
	return fmt.Sprintf("%s.%s", volumeID, diskFormat)
}

// VolumeID returns the ID in a /<volume-id>.<ext> path.
func VolumeID(path string) (string, bool) {
	name, ok := strings.CutPrefix(path, "/")
	if !ok {
		return "", false
	}
	matches := diskPattern.FindStringSubmatch(name)
	if len(matches) != 3 {
		return "", false
	}
	id := matches[1]
	if !common.IDFormat.MatchString(id) {
		return "", false
	}
	return id, true
}
