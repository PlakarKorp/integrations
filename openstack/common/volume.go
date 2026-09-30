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
	"slices"
	"sync"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	gcopenstack "github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/snapshots"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
)

// Creating the volume image.
// VolumeImage is a Glance image of a Cinder volume, taken from a snapshot.
// Volume -> Snapshot -> Temporary Volume -> Glance Image.
type VolumeImage struct {
	Volume   *volumes.Volume
	Snapshot *snapshots.Snapshot
	Image    *images.Image
}

func (c *Client) GetVolume(ctx context.Context, volumeID string) (*volumes.Volume, error) {
	block, err := c.service("volumes", gcopenstack.NewBlockStorageV3)
	if err != nil {
		return nil, err
	}
	vol, err := volumes.Get(ctx, block, volumeID).Extract()
	if err != nil {
		return nil, fmt.Errorf("get volume %q: %w", volumeID, err)
	}
	return vol, nil
}

// CreateVolumeImage snapshots the volume, creates a temporary volume from the
// snapshot, and uploads that to Glance: no API reads a snapshot's bytes. Each
// resource goes into cleanup as soon as it exists, so a failure leaves nothing
// behind once cleanup runs.
func (c *Client) CreateVolumeImage(ctx context.Context, volumeID string, cleanup *Cleanup) (*VolumeImage, error) {
	block, err := c.service("volumes", gcopenstack.NewBlockStorageV3)
	if err != nil {
		return nil, err
	}
	image, err := c.service("images", gcopenstack.NewImageV2)
	if err != nil {
		return nil, err
	}
	// These clients are reused to poll the resources' status and delete them,
	// so they must be kept until cleanup runs.
	cleanup.block, cleanup.image = block, image

	vol, err := c.GetVolume(ctx, volumeID)
	if err != nil {
		return nil, err
	}

	created, err := snapshots.Create(ctx, block, snapshots.CreateOpts{
		VolumeID: volumeID,
		Name:     fmt.Sprintf("plakar-backup-%s-%d", volumeID, time.Now().UTC().UnixNano()),
		Force:    true, // required for in-use volumes; the snapshot is crash-consistent
	}).Extract()
	if err != nil {
		return nil, fmt.Errorf("snapshot volume %q: %w", volumeID, err)
	}
	cleanup.snapshotID = created.ID
	// Wait for the snapshot to become available before creating a temporary
	// volume from it.
	snap, err := waitStatus(ctx, "snapshot", created.ID, getSnapshot(block, created.ID), "available", "error")
	if err != nil {
		return nil, err
	}

	tmp, err := volumes.Create(ctx, block, volumes.CreateOpts{
		SnapshotID: snap.ID,
		Name:       "plakar-tmp-" + snap.ID,
		Size:       snap.Size,
	}, nil).Extract()
	if err != nil {
		return nil, fmt.Errorf("create volume from snapshot %q: %w", snap.ID, err)
	}
	cleanup.volumeID = tmp.ID
	// Wait for the temporary volume to become available before uploading it to
	// Glance.
	if _, err := waitStatus(ctx, "volume", tmp.ID, getVolume(block, tmp.ID), "available", "error"); err != nil {
		return nil, err
	}

	upload, err := volumes.UploadImage(ctx, block, tmp.ID, volumes.UploadImageOpts{
		ImageName:       "plakar-tmp-" + snap.ID,
		DiskFormat:      "qcow2",
		ContainerFormat: "bare",
	}).Extract()
	if err != nil {
		return nil, fmt.Errorf("upload volume %q to an image: %w", tmp.ID, err)
	}
	cleanup.imageID = upload.ImageID
	// Wait for the image to become active before returning it, so that plakar
	// can read it.
	img, err := waitStatus(ctx, "image", upload.ImageID, getImage(image, upload.ImageID), "active", "killed", "error")
	if err != nil {
		return nil, err
	}

	return &VolumeImage{Volume: vol, Snapshot: snap, Image: img}, nil
}

// Cleanup records the temporary resources of one volume backup: the
// snapshot, the temporary volume and the image.
type Cleanup struct {
	mu                            sync.Mutex
	block, image                  *gophercloud.ServiceClient
	snapshotID, volumeID, imageID string
}

// Polling resource status.

// getter fetches a resource and its status.
type getter[T any] func(context.Context) (T, string, error)

func getVolume(block *gophercloud.ServiceClient, id string) getter[*volumes.Volume] {
	return func(ctx context.Context) (*volumes.Volume, string, error) {
		v, err := volumes.Get(ctx, block, id).Extract()
		if err != nil {
			return nil, "", err
		}
		return v, v.Status, nil
	}
}

func getSnapshot(block *gophercloud.ServiceClient, id string) getter[*snapshots.Snapshot] {
	return func(ctx context.Context) (*snapshots.Snapshot, string, error) {
		s, err := snapshots.Get(ctx, block, id).Extract()
		if err != nil {
			return nil, "", err
		}
		return s, s.Status, nil
	}
}

func getImage(image *gophercloud.ServiceClient, id string) getter[*images.Image] {
	return func(ctx context.Context) (*images.Image, string, error) {
		i, err := images.Get(ctx, image, id).Extract()
		if err != nil {
			return nil, "", err
		}
		return i, string(i.Status), nil
	}
}

// waitStatus waits for the resource to reach want, and fails once it reaches
// one of failed.
func waitStatus[T any](ctx context.Context, kind, id string, get getter[T], want string, failed ...string) (T, error) {
	var got T
	err := gophercloud.WaitFor(ctx, func(ctx context.Context) (bool, error) {
		r, status, err := get(ctx)
		if err != nil {
			return false, fmt.Errorf("get %s %q: %w", kind, id, err)
		}
		if slices.Contains(failed, status) {
			return false, fmt.Errorf("%s %q entered %s", kind, id, status)
		}
		got = r
		return status == want, nil
	})
	return got, err
}
