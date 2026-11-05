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
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/PlakarKorp/integrations/openstack/common/attach"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
)

// Restoring a volume through a block device, the counterpart of
// NewCinderDisk: a new volume is created, attached to the host plakar runs
// on, and the disk's bytes written to the device it turns up as. Nothing
// goes through Glance.

// ErrDiskNotWritable is ValidateWriteAttachment's error: the disk's bytes
// are not a block device's content, so they cannot be written to one. Such a
// disk goes through Glance.
var ErrDiskNotWritable = errors.New("disk cannot be written to a block device")

// ValidateWriteAttachment reports whether the disk can be written to a block
// device as it is: only a raw disk can, and only in the clear. A qcow2 disk needs
// converting, which Glance and Cinder do; a raw disk read through Glance from
// an encrypted volume is ciphertext under the key its image properties name
// (see EncryptionImageProperties), and the hypervisor would encrypt it again.
func ValidateWriteAttachment(metadata *DiskMetadata) error {
	if format := DiskFormat(metadata); format != RawDiskFormat {
		return fmt.Errorf("%w: it is %s", ErrDiskNotWritable, format)
	}
	if props := RestoreImageProperties(metadata); len(props) > 0 {
		return fmt.Errorf("%w: it is encrypted under key %q", ErrDiskNotWritable, props[encryptionImageProperties[0]])
	}
	return nil
}

// VolumeWrite is a new, empty volume attached to the host, with the device
// it turns up as checked writable, waiting for the disk's bytes.
type VolumeWrite struct {
	volume  *volumes.Volume
	device  string
	cleanup Cleanup
}

// AttachRestoreVolume creates a volume shaped like metadata's, attaches it
// to host and finds the device it turns up as. It fails before any of the
// disk's bytes are read, with the volume deleted again, so the caller can
// fall back to Glance: with ErrDiskNotWritable when the disk is no block
// device's content, or when Nova refuses the attach, no device turns up or
// the process may not write it.
func (c *Client) AttachRestoreVolume(ctx context.Context, host *servers.Server, metadata *DiskMetadata) (*VolumeWrite, error) {
	if err := ValidateWriteAttachment(metadata); err != nil {
		return nil, err
	}
	block, err := c.cinder()
	if err != nil {
		return nil, err
	}
	vol, err := c.emptyVolume(ctx, metadata.Volume)
	if err != nil {
		return nil, err
	}

	w := &VolumeWrite{volume: vol}
	w.cleanup.block, w.cleanup.volumeID = block, vol.ID
	device, err := c.volumeBlockDevice(ctx, host, vol, &w.cleanup, attach.DeviceWritable)
	if err != nil {
		w.cleanup.Run(ctx)
		return nil, err
	}
	w.device = device
	return w, nil
}

// Volume is the volume being restored, in use by the host until Copy
// detaches it.
func (w *VolumeWrite) Volume() *volumes.Volume { return w.volume }

// Copy writes r to the device, flushes it and detaches the volume, which is
// then the restored one, available. A failure to write deletes the volume: r
// is partly consumed by then, so there is no falling back. A failure to
// detach leaves the volume, written, for the user to detach by hand.
func (w *VolumeWrite) Copy(ctx context.Context, r io.Reader) (*volumes.Volume, error) {
	if err := writeDevice(w.device, r); err != nil {
		w.cleanup.Run(ctx)
		return nil, err
	}
	if err := w.cleanup.Detach(ctx); err != nil {
		return nil, fmt.Errorf("restored volume %q is written but still attached to the host: %w", w.volume.ID, err)
	}
	return w.volume, nil
}

// writeBufferSize suits a block device better than io.Copy's 32 KiB.
const writeBufferSize = 1 << 20

// writeDevice copies r to the device and syncs it, so the bytes are on the
// volume before it is detached. A disk larger than the volume fails with
// ENOSPC; a smaller one leaves the rest of the fresh volume as it is.
func writeDevice(device string, r io.Reader) error {
	f, err := os.OpenFile(device, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	// *os.File's own ReadFrom would ignore the buffer.
	if _, err := io.CopyBuffer(struct{ io.Writer }{f}, r, make([]byte, writeBufferSize)); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", device, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync %s: %w", device, err)
	}
	return f.Close()
}
