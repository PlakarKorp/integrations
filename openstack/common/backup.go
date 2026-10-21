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
	"log"

	"github.com/PlakarKorp/integrations/openstack/common/attach"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/snapshots"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
)

// Disk is a volume's content, and the metadata a restore recreates it from.
type Disk struct {
	Metadata *DiskMetadata
	Reader   func() (io.ReadCloser, error)
}

// ReadVolume snapshots a volume, creates a temporary volume, returns back the appropriate disk
// depending if we can attach to the execution host or not
func (c *Client) ReadVolume(ctx context.Context, vol *volumes.Volume, cleanup *Cleanup) (*Disk, error) {
	snap, err := c.SnapshotVolume(ctx, vol.ID, cleanup)
	if err != nil {
		return nil, err
	}
	tmp, err := c.CreateVolumeFromSnapshot(ctx, snap, cleanup)
	if err != nil {
		return nil, err
	}

	device, err := c.attachToHost(ctx, vol, tmp, cleanup)
	// NOTE: if volume is attached, we need to return a different reader on the disk
	if err == nil {
		return c.deviceDisk(ctx, vol, snap, device, cleanup), nil
	}

	// Only attach errors can be ignored, it
	if !errors.Is(err, attach.ErrNotAttachable) {
		log.Printf("openstack: volume %q: %v; reading it through Glance", vol.ID, err)
	}
	return c.imageDisk(ctx, vol, snap, tmp, cleanup)
}

// deviceDisk is the temporary volume read from the block device it is
// attached as: raw, through no image, and for an encrypted volume with no
// key to carry, as the bytes are in the clear.
func (c *Client) deviceDisk(ctx context.Context, vol *volumes.Volume, snap *snapshots.Snapshot, device string, cleanup *Cleanup) *Disk {
	return &Disk{
		Metadata: &DiskMetadata{
			Origin:          DiskOriginCinder,
			DiskFormat:      RawDiskFormat,
			Volume:          vol,
			Snapshot:        snap,
			Image:           nil,
			ImageProperties: nil,
		},
		Reader: c.DeviceReader(ctx, device, cleanup),
	}
}

// imageDisk is used when directly attaching volume wont work, e.g. the host is off the cloud.
// Before returning the Disk, it uploads the volume to a glance image, which is the only way to read a volume outside the perimiters
func (c *Client) imageDisk(ctx context.Context, vol *volumes.Volume, snap *snapshots.Snapshot, tmp *volumes.Volume, cleanup *Cleanup) (*Disk, error) {
	img, err := c.UploadVolumeImage(ctx, tmp, cleanup)
	if err != nil {
		return nil, err
	}
	return &Disk{
		Metadata: &DiskMetadata{
			Origin:          DiskOriginCinder,
			DiskFormat:      img.DiskFormat,
			Image:           img,
			ImageProperties: EncryptionImageProperties(img),
			Volume:          vol,
			Snapshot:        snap,
		},
		Reader: c.ImageReader(ctx, img.ID, cleanup),
	}, nil
}

// attachToHost attaches the temporary volume to the host plakar runs on,
// when that is a Nova server of the cloud, and returns the block device it
// is readable at. The error is attach.ErrNotAttachable when the host is no
// such server: the usual case, off the cloud. When the attach fails, no
// device turns up or it cannot be read, the volume is detached again and
// left available for the Glance path.
func (c *Client) attachToHost(ctx context.Context, vol, tmp *volumes.Volume, cleanup *Cleanup) (string, error) {
	host, err := attach.Preflight(ctx, c, c.Scope().ProjectID, vol)
	if err != nil {
		return "", err
	}
	var device string
	err = c.AttachTempVolume(ctx, host.ID, tmp, cleanup)
	if err == nil {
		device, err = attach.Device(ctx, tmp.ID)
	}
	if err != nil {
		// Detach is a no-op when Nova never took the attach.
		if detachErr := cleanup.Detach(ctx); detachErr != nil {
			err = fmt.Errorf("%w; detaching: %w", err, detachErr)
		}
		return "", fmt.Errorf("attaching to server %q: %w", host.ID, err)
	}
	return device, nil
}
