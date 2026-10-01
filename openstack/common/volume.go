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
	"log"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	gcopenstack "github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/snapshots"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
)

func (c *Client) cinder() (*gophercloud.ServiceClient, error) {
	return c.service("volumes", gcopenstack.NewBlockStorageV3)
}

func (c *Client) glance() (*gophercloud.ServiceClient, error) {
	return c.service("images", gcopenstack.NewImageV2)
}

// Creating the volume image.

// VolumeImage is a Glance image of a Cinder volume, taken from a snapshot.
type VolumeImage struct {
	Volume   *volumes.Volume
	Snapshot *snapshots.Snapshot
	Image    *images.Image
}

func (c *Client) GetVolume(ctx context.Context, volumeID string) (*volumes.Volume, error) {
	block, err := c.cinder()
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
	block, err := c.cinder()
	if err != nil {
		return nil, err
	}
	image, err := c.glance()
	if err != nil {
		return nil, err
	}
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
	img, err := waitStatus(ctx, "image", upload.ImageID, getImage(image, upload.ImageID), "active", "killed", "error")
	if err != nil {
		return nil, err
	}

	return &VolumeImage{Volume: vol, Snapshot: snap, Image: img}, nil
}

// Deleting the temporary resources.

// Defaults, as variables so tests can shorten them. waitTimeout suits volumes
// of a few hundred GiB; it can become a connector option once larger ones need
// it.
var (
	deleteTimeout = 30 * time.Second // one delete request
	waitTimeout   = 15 * time.Minute // Cinder finishing what blocks a delete
)

// Cleanup deletes the temporary resources of one volume backup: the image,
// the temporary volume, then the snapshot it was made from. It tries each once
// and logs a failure, as plakar ignores the errors of both the disk's Close and
// the importer's.
type Cleanup struct {
	mu                            sync.Mutex
	block, image                  *gophercloud.ServiceClient
	snapshotID, volumeID, imageID string
}

// Run ignores cancellation of ctx, so it still cleans up once Import's context
// is done. It cannot run if plakar kills the plugin, as it does on Ctrl-C or
// SIGTERM: the resources are then left behind.
func (c *Cleanup) Run(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx = context.WithoutCancel(ctx)

	if c.imageID != "" {
		logFailure("image", c.imageID, c.deleteImage(ctx))
		c.imageID = ""
	}

	volumeDeleted := false
	if c.volumeID != "" {
		err := c.deleteVolume(ctx)
		logFailure("volume", c.volumeID, err)
		volumeDeleted = isDeleted(err)
	}

	if c.snapshotID != "" {
		logFailure("snapshot", c.snapshotID, c.deleteSnapshot(ctx, volumeDeleted))
		c.snapshotID = ""
	}
	c.volumeID = ""
}

func (c *Cleanup) deleteImage(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, deleteTimeout)
	defer cancel()
	return images.Delete(ctx, c.image, c.imageID).ExtractErr()
}

func (c *Cleanup) deleteVolume(ctx context.Context) error {
	if err := waitDeletable(ctx, getVolume(c.block, c.volumeID)); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, deleteTimeout)
	defer cancel()
	return volumes.Delete(ctx, c.block, c.volumeID, volumes.DeleteOpts{}).ExtractErr()
}

func (c *Cleanup) deleteSnapshot(ctx context.Context, volumeDeleted bool) error {
	if volumeDeleted {
		// Wait for the temporary volume to be gone before deleting the snapshot,
		// as some backends block it until then.
		if err := waitGone(ctx, getVolume(c.block, c.volumeID)); err != nil {
			return err
		}
	}
	// Cinder refuses to delete a snapshot that is still creating.
	if err := waitDeletable(ctx, getSnapshot(c.block, c.snapshotID)); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, deleteTimeout)
	defer cancel()
	return snapshots.Delete(ctx, c.block, c.snapshotID).ExtractErr()
}

// isDeleted counts a resource already gone as deleted.
func isDeleted(err error) bool {
	return err == nil || gophercloud.ResponseCodeIs(err, http.StatusNotFound)
}

func logFailure(kind, id string, err error) {
	if !isDeleted(err) {
		log.Printf("openstack: cleanup: delete %s %q: %v", kind, id, err)
	}
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

// waitDeletable waits for a Cinder resource to leave its transient states:
// Cinder refuses to delete a snapshot or volume that is still creating or
// uploading.
func waitDeletable[T any](ctx context.Context, get getter[T]) error {
	ctx, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()
	return gophercloud.WaitFor(ctx, func(ctx context.Context) (bool, error) {
		_, s, err := get(ctx)
		return s == "available" || s == "error", err
	})
}

func waitGone[T any](ctx context.Context, get getter[T]) error {
	ctx, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()
	return gophercloud.WaitFor(ctx, func(ctx context.Context) (bool, error) {
		_, _, err := get(ctx)
		if gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
			return true, nil
		}
		return false, err
	})
}
