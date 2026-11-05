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

import "fmt"

// DiskTransfer is how a disk's bytes move between plakar and a Cinder
// volume: through a block device of the Nova server plakar runs on, the
// volume attached to it, or through a temporary Glance image.
type DiskTransfer string

const (
	// TransferAuto attaches when the host is a Nova server of the cloud that
	// takes the volume, and goes through Glance otherwise. The default.
	TransferAuto DiskTransfer = "auto"
	// TransferAttach attaches, and fails when it cannot.
	TransferAttach DiskTransfer = "attach"
	// TransferGlance goes through Glance without trying to attach.
	TransferGlance DiskTransfer = "glance"
)

// ParseDiskTransfer parses the openstack_disk_transfer parameter; unset is
// TransferAuto.
func ParseDiskTransfer(input string) (DiskTransfer, error) {
	switch t := DiskTransfer(input); t {
	case "":
		return TransferAuto, nil
	case TransferAuto, TransferAttach, TransferGlance:
		return t, nil
	default:
		return "", fmt.Errorf("bad value: %q is not %s, %s or %s", t, TransferAuto, TransferAttach, TransferGlance)
	}
}
