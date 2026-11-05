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
	"fmt"
	"io"
	"log"

	"github.com/PlakarKorp/integrations/openstack/common/attach"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/snapshots"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
)

// Disk is a volume's content, and the metadata a restore recreates it from.
type Disk struct {
	Metadata *DiskMetadata
	Reader   func() (io.ReadCloser, error)
}

// PreflightHost identifies the executing host as a Nova server that
// temporary volumes can be attached to.
func (c *Client) PreflightHost(ctx context.Context) (*servers.Server, error) {
	return attach.Preflight(ctx, c, c.Scope().ProjectID)
}

// TempVolumeOpts describes a temporary root volume in the server's
// availability zone, sized from the saved flavor.
func TempVolumeOpts(server *servers.Server, metadata *ServerMetadata) volumes.CreateOpts {
	opts := volumes.CreateOpts{}
	if server != nil {
		opts.AvailabilityZone = server.AvailabilityZone
	}
	if metadata != nil && metadata.Flavor != nil {
		opts.Size = metadata.Flavor.Disk
	}
	return opts
}

// ReadCinderDisk reads an already prepared snapshot clone through host when
// one was found, falling back to uploading the clone to Glance.
func (c *Client) ReadCinderDisk(ctx context.Context, host *servers.Server, vol *volumes.Volume, snap *snapshots.Snapshot, tmp *volumes.Volume, cleanup *Cleanup) (*Disk, error) {
	metadata := CinderDiskMetadata(vol, snap)
	if host != nil {
		disk, err := c.NewCinderDisk(ctx, host, tmp, metadata, cleanup)
		if err == nil {
			return disk, nil
		}
		log.Printf("openstack: volume %q: %v; reading it through Glance", vol.ID, err)
	}
	img, err := c.UploadVolumeImage(ctx, tmp, cleanup)
	if err != nil {
		return nil, err
	}
	return c.NewGlanceDisk(ctx, img, metadata, cleanup), nil
}

// ReadImageDisk returns a disk downloaded directly from img.
func (c *Client) NewGlanceDisk(ctx context.Context, img *images.Image, metadata *DiskMetadata, cleanup *Cleanup) *Disk {
	m := *metadata
	m.DiskFormat = img.DiskFormat
	m.Image = img
	m.ImageProperties = EncryptionImageProperties(img)
	return &Disk{
		Metadata: &m,
		Reader:   c.ImageReader(ctx, img.ID, cleanup),
	}
}

// NewCinderDisk attaches tmp to host and returns a raw disk read from the
// block device it appears as.
func (c *Client) NewCinderDisk(ctx context.Context, host *servers.Server, volume *volumes.Volume, metadata *DiskMetadata, cleanup *Cleanup) (*Disk, error) {
	device, err := c.volumeBlockDevice(ctx, host, volume, cleanup, attach.DeviceReadable)
	if err != nil {
		return nil, err
	}
	m := *metadata
	m.DiskFormat = RawDiskFormat
	m.ImageProperties = nil
	return &Disk{
		Metadata: &m,
		Reader:   c.DeviceReader(ctx, device, cleanup),
	}, nil
}

// volumeBlockDevice attaches volume to host and returns the block device it
// turns up as, found and checked by validator: attach.DeviceReadable for a
// backup that reads it, attach.DeviceWritable for a restore that writes it.
// When attachment fails, no device turns up, or validator refuses it, the volume is
// detached and left available for the caller's fallback path.
func (c *Client) volumeBlockDevice(ctx context.Context, host *servers.Server, volume *volumes.Volume, cleanup *Cleanup, validator func(context.Context, string) (string, error)) (string, error) {
	var device string
	err := c.AttachTempVolume(ctx, host.ID, volume, cleanup)
	if err == nil {
		device, err = validator(ctx, volume.ID)
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
