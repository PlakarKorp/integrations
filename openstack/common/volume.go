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
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	gcopenstack "github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/snapshots"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/volumeattach"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/imagedata"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
)

const (
	serviceVolumes = "volumes"
	serviceImages  = "images"

	kindVolume     = "volume"
	kindSnapshot   = "snapshot"
	kindImage      = "image"
	kindAttachment = "attachment"

	statusAvailable      = "available"
	statusInUse          = "in-use"
	statusUploading      = "uploading"
	statusError          = "error"
	statusErrorAttaching = "error_attaching"
	statusActive         = "active"
	statusKilled         = "killed"

	// Names of the resources a backup and a restore create.
	backupPrefix  = "plakar-backup-"
	tmpPrefix     = "plakar-tmp-"
	RestorePrefix = "plakar-restore-"

	// DefaultDiskFormat is the format backups upload to Glance, and the one
	// assumed when .METADATA.json records none. RawDiskFormat is a block
	// device's content as read, and the only format Cinder uploads an
	// encrypted volume in.
	DefaultDiskFormat = "qcow2"
	RawDiskFormat     = "raw"
	containerFormat   = "bare"
)

func (c *Client) cinder() (*gophercloud.ServiceClient, error) {
	return c.service(serviceVolumes, gcopenstack.NewBlockStorageV3)
}

func (c *Client) glance() (*gophercloud.ServiceClient, error) {
	return c.service(serviceImages, gcopenstack.NewImageV2)
}

// Reading a volume's bytes. No API reads a snapshot's, so a backup clones the
// snapshot into a temporary volume, then either attaches that to the host
// plakar runs on or uploads it to a Glance image; ReadVolume (backup.go)
// chooses.

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

// SnapshotVolume snapshots the volume and waits for the snapshot to become
// available. The snapshot goes into cleanup as soon as it exists, so a failure
// leaves nothing behind once cleanup runs.
func (c *Client) SnapshotVolume(ctx context.Context, volumeID string, cleanup *Cleanup) (*snapshots.Snapshot, error) {
	block, err := c.cinder()
	if err != nil {
		return nil, err
	}
	cleanup.block = block

	created, err := snapshots.Create(ctx, block, snapshots.CreateOpts{
		VolumeID: volumeID,
		Name:     fmt.Sprintf("%s%s-%d", backupPrefix, volumeID, time.Now().UTC().UnixNano()),
		Force:    true, // required for in-use volumes; the snapshot is crash-consistent
	}).Extract()
	if err != nil {
		return nil, fmt.Errorf("snapshot volume %q: %w", volumeID, err)
	}
	cleanup.snapshotID = created.ID
	return waitStatus(ctx, kindSnapshot, created.ID, getSnapshot(block, created.ID), statusAvailable, statusError)
}

// CreateVolumeFromSnapshot clones the snapshot into a temporary volume and
// waits for it to become available. The snapshot is usually SnapshotVolume's,
// but any available one does. The volume goes into cleanup as soon as it
// exists, so a failure leaves nothing behind once cleanup runs.
func (c *Client) CreateVolumeFromSnapshot(ctx context.Context, snap *snapshots.Snapshot, cleanup *Cleanup) (*volumes.Volume, error) {
	block, err := c.cinder()
	if err != nil {
		return nil, err
	}
	cleanup.block = block

	tmp, err := volumes.Create(ctx, block, volumes.CreateOpts{
		SnapshotID: snap.ID,
		Name:       tmpPrefix + snap.ID,
		Size:       snap.Size,
	}, nil).Extract()
	if err != nil {
		return nil, fmt.Errorf("create volume from snapshot %q: %w", snap.ID, err)
	}
	cleanup.volumeID = tmp.ID
	return waitStatus(ctx, kindVolume, tmp.ID, getVolume(block, tmp.ID), statusAvailable, statusError)
}

// UploadVolumeImage uploads the temporary volume to a Glance image and waits
// for it to become active. The image goes into cleanup as soon as it exists.
// A clone of an encrypted volume is of the same encrypted type, and Cinder
// then uploads the ciphertext as-is: the image carries the key id (see
// EncryptionImageProperties), and only the raw format is allowed.
func (c *Client) UploadVolumeImage(ctx context.Context, tmp *volumes.Volume, cleanup *Cleanup) (*images.Image, error) {
	block, err := c.cinder()
	if err != nil {
		return nil, err
	}
	image, err := c.glance()
	if err != nil {
		return nil, err
	}
	cleanup.block, cleanup.image = block, image

	// Cinder before 2025.1 (LP#2144550) sends Glance properties={} when the
	// source's only custom image key is reserved, e.g. signature_verified, and
	// Glance rejects it. A marker on our own temp volume keeps it non-empty.
	if err := volumes.SetImageMetadata(ctx, block, tmp.ID, volumes.ImageMetadataOpts{
		Metadata: map[string]string{"plakar_backup": "true"},
	}).ExtractErr(); err != nil {
		return nil, fmt.Errorf("mark temporary volume %q: %w", tmp.ID, err)
	}

	diskFormat := DefaultDiskFormat
	if tmp.Encrypted {
		diskFormat = RawDiskFormat
	}
	upload, err := volumes.UploadImage(ctx, block, tmp.ID, volumes.UploadImageOpts{
		ImageName:       tmpPrefix + tmp.ID,
		DiskFormat:      diskFormat,
		ContainerFormat: containerFormat,
	}).Extract()
	if err != nil {
		return nil, fmt.Errorf("upload volume %q to an image: %w", tmp.ID, err)
	}
	cleanup.imageID = upload.ImageID
	return waitStatus(ctx, kindImage, upload.ImageID, getUploadedImage(block, image, tmp.ID, upload.ImageID), statusActive, statusKilled, statusError)
}

// getUploadedImage reads the image Cinder is uploading tmp to. Cinder
// deletes the image when the upload fails on its side (a conversion to
// qcow2 with no room for it on the Cinder host, say), so a 404 on it is the
// upload's failure, not a transient error, once the volume is no longer
// uploading. While it still is, Glance may just be behind.
func getUploadedImage(block, image *gophercloud.ServiceClient, volumeID, imageID string) getter[*images.Image] {
	get := getImage(image, imageID)
	return func(ctx context.Context) (*images.Image, string, error) {
		img, status, err := get(ctx)
		if !gophercloud.ResponseCodeIs(err, http.StatusNotFound) {
			return img, status, err
		}
		vol, volErr := volumes.Get(ctx, block, volumeID).Extract()
		switch {
		case volErr != nil:
			return nil, "", err
		case vol.Status == statusUploading:
			return nil, statusUploading, nil
		}
		return nil, "", &finalError{fmt.Errorf("upload volume %q to image %q: Cinder deleted the image, so the upload failed on its side (the volume is %s); see cinder-volume's log", volumeID, imageID, vol.Status)}
	}
}

// attachTimeout bounds AttachTempVolume's wait for Nova to finish an
// attachment, which normally takes seconds. Nova's attach is asynchronous:
// one that stalls, or that Nova rolls back to available, would otherwise be
// polled for as long as the caller's context lives, which for an import is
// forever. A variable so tests can shorten it.
var attachTimeout = 5 * time.Minute

// AttachTempVolume attaches the temporary volume to the host server, for
// reading it from a local block device, and waits for it to be in use. The
// attachment goes into cleanup, which detaches before deleting the volume.
// Cleanup.Detach undoes it alone, for when no device turns up, or when the
// wait fails: the wait is bounded by a timeout and to nova returning error_attaching.
func (c *Client) AttachTempVolume(ctx context.Context, hostID string, tmp *volumes.Volume, cleanup *Cleanup) error {
	block, err := c.cinder()
	if err != nil {
		return err
	}
	nova, err := c.nova()
	if err != nil {
		return err
	}
	if cleanup.volumeID != tmp.ID {
		return fmt.Errorf("attach volume %q: it is not cleanup's temporary volume", tmp.ID)
	}
	cleanup.block, cleanup.nova = block, nova

	if _, err := volumeattach.Create(ctx, nova, hostID, volumeattach.CreateOpts{VolumeID: tmp.ID}).Extract(); err != nil {
		return fmt.Errorf("attach volume %q to server %q: %w", tmp.ID, hostID, err)
	}
	cleanup.attachedTo = hostID
	waitCtx, cancel := context.WithTimeout(ctx, attachTimeout)
	defer cancel()
	_, err = waitStatus(waitCtx, kindVolume, tmp.ID, getVolume(block, tmp.ID), statusInUse, statusError, statusErrorAttaching)
	return err
}

// Deleting the temporary resources.

// Defaults, as variables so tests can shorten them. waitTimeout suits volumes
// of a few hundred GiB; it can become a connector option once larger ones need
// it.
var (
	deleteTimeout = 30 * time.Second // one delete request
	waitTimeout   = 15 * time.Minute // Cinder finishing what blocks a delete
)

// Cleanup deletes temporary resources: the image, the temporary volume (first
// detaching it from the host it was attached to), then the snapshot it was
// made from, skipping those it does not hold. It tries each once and logs a
// failure, as plakar ignores the errors of the importer's and exporter's
// Close.
type Cleanup struct {
	mu                            sync.Mutex
	block, image, nova            *gophercloud.ServiceClient
	snapshotID, volumeID, imageID string
	attachedTo                    string // the server volumeID is attached to
}

// Run ignores cancellation of ctx, so it still cleans up once the backup's or
// restore's context is done. It cannot run if plakar kills the plugin, as it
// does on Ctrl-C or SIGTERM: the resources are then left behind.
func (c *Cleanup) Run(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx = context.WithoutCancel(ctx)

	if c.attachedTo != "" {
		logFailure(kindAttachment, c.volumeID, c.detach(ctx))
	}

	if c.imageID != "" {
		logFailure(kindImage, c.imageID, c.deleteImage(ctx))
		c.imageID = ""
	}

	volumeDeleted := false
	if c.volumeID != "" {
		err := c.deleteVolume(ctx)
		logFailure(kindVolume, c.volumeID, err)
		volumeDeleted = isDeleted(err)
	}

	if c.snapshotID != "" {
		logFailure(kindSnapshot, c.snapshotID, c.deleteSnapshot(ctx, volumeDeleted))
		c.snapshotID = ""
	}
	c.volumeID = ""
}

// Detach detaches the temporary volume from the host, if it is attached, and
// waits for Cinder to report it available again, so it can be uploaded or
// deleted. The rest of Cleanup is untouched.
func (c *Cleanup) Detach(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.attachedTo == "" {
		return nil
	}
	return c.detach(context.WithoutCancel(ctx))
}

// detach forgets the attachment whether or not Nova accepted the detach: like
// a delete, it is tried once.
func (c *Cleanup) detach(ctx context.Context) error {
	serverID := c.attachedTo
	c.attachedTo = ""

	reqCtx, cancel := context.WithTimeout(ctx, deleteTimeout)
	defer cancel()
	err := volumeattach.Delete(reqCtx, c.nova, serverID, c.volumeID).ExtractErr()
	if !isDeleted(err) {
		return err
	}
	// Cinder reports the volume detaching for a while, then available.
	return waitDeletable(ctx, getVolume(c.block, c.volumeID))
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

// failedStatusError is waitStatus's error when the resource reaches a failed
// status.
type failedStatusError struct {
	kind, id, status string
}

func (e *failedStatusError) Error() string {
	return fmt.Sprintf("%s %q entered %s", e.kind, e.id, e.status)
}

// finalError is a get error waitStatus gives up on at once, instead of
// taking it for a transient one.
type finalError struct{ err error }

func (e *finalError) Error() string { return e.err.Error() }
func (e *finalError) Unwrap() error { return e.err }

// Consecutive transient get errors waitStatus tolerates before giving up.
var maxTransientGetFailures = 5

// waitStatus waits for the resource to reach want, and fails with a
// *failedStatusError once it reaches one of failed. A single get error does
// not abort it, up to maxTransientGetFailures, unless it is a *finalError.
func waitStatus[T any](ctx context.Context, kind, id string, get getter[T], want string, failed ...string) (T, error) {
	var got T
	failures := 0
	err := gophercloud.WaitFor(ctx, func(ctx context.Context) (bool, error) {
		r, status, err := get(ctx)
		if err != nil {
			if final, ok := errors.AsType[*finalError](err); ok {
				return false, final.err
			}
			failures++
			if failures > maxTransientGetFailures {
				return false, fmt.Errorf("get %s %q: %w", kind, id, err)
			}
			return false, nil
		}
		failures = 0
		if slices.Contains(failed, status) {
			return false, &failedStatusError{kind: kind, id: id, status: status}
		}
		got = r
		return status == want, nil
	})
	// WaitFor returns the context's error as it is, which names nothing.
	if err != nil && ctx.Err() != nil {
		err = fmt.Errorf("wait for %s %q to be %s: %w", kind, id, want, err)
	}
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
		return s == statusAvailable || s == statusError, err
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

// Restoring a volume from an image.

// Some Cinder backends put a fresh volume in "error".
const volumeAttempts = 3

// requiredSizeGiB rounds a byte size up to whole GiB, the unit Cinder uses to
// reject a volume smaller than the image it is created from.
func requiredSizeGiB(bytes int64) int {
	const gib = 1 << 30
	return int((bytes + gib - 1) / gib)
}

// UploadTempImage uploads r to a new private image and waits for it to become
// active. properties is set on the image as-is, e.g. to carry over the
// cinder_encryption_key_id of an encrypted source volume. It deletes the
// image on failure.
func (c *Client) UploadTempImage(ctx context.Context, name string, r io.Reader, diskFormat string, properties map[string]string) (*images.Image, error) {
	image, err := c.glance()
	if err != nil {
		return nil, err
	}

	visibility := images.ImageVisibilityPrivate
	created, err := images.Create(ctx, image, images.CreateOpts{
		Name:            name,
		ContainerFormat: containerFormat,
		DiskFormat:      diskFormat,
		Visibility:      &visibility,
		Properties:      properties,
	}).Extract()
	if err != nil {
		return nil, fmt.Errorf("create image %q: %w", name, err)
	}

	// net/http closes a request body that is an io.ReadCloser; r is the
	// caller's to close, and plakar's record reader panics if closed twice.
	if err := imagedata.Upload(ctx, image, created.ID, struct{ io.Reader }{r}).ExtractErr(); err != nil {
		(&Cleanup{image: image, imageID: created.ID}).Run(ctx)
		return nil, fmt.Errorf("upload image %q: %w", created.ID, err)
	}

	// Cinder only creates a volume from an active image.
	img, err := waitStatus(ctx, kindImage, created.ID, getImage(image, created.ID), statusActive, statusKilled, statusError)
	if err != nil {
		(&Cleanup{image: image, imageID: created.ID}).Run(ctx)
		return nil, err
	}
	return img, nil
}

// DeleteTempImage logs a failure, as Cleanup does.
func (c *Client) DeleteTempImage(ctx context.Context, imageID string) {
	image, err := c.glance()
	if err != nil {
		logFailure(kindImage, imageID, err)
		return
	}
	(&Cleanup{image: image, imageID: imageID}).Run(ctx)
}

// DeleteTempVolume logs a failure, as Cleanup does.
func (c *Client) DeleteTempVolume(ctx context.Context, volumeID string) {
	block, err := c.cinder()
	if err != nil {
		logFailure(kindVolume, volumeID, err)
		return
	}
	(&Cleanup{block: block, volumeID: volumeID}).Run(ctx)
}

// createVolume creates a volume from opts. It raises an image-backed
// volume's size when the image requires more space. A volume that does not
// become available is deleted, and one in "error" is created again.
func (c *Client) createVolume(ctx context.Context, opts volumes.CreateOpts) (*volumes.Volume, error) {
	block, err := c.cinder()
	if err != nil {
		return nil, err
	}
	if opts.Size <= 0 {
		return nil, errors.New("create volume: no size")
	}

	if opts.ImageID != "" {
		image, err := c.glance()
		if err != nil {
			return nil, err
		}
		img, err := images.Get(ctx, image, opts.ImageID).Extract()
		if err != nil {
			return nil, fmt.Errorf("get image %q: %w", opts.ImageID, err)
		}
		if minSize := requiredSizeGiB(img.SizeBytes); minSize > opts.Size {
			opts.Size = minSize
		}
	}

	for attempt := 1; ; attempt++ {
		created, err := volumes.Create(ctx, block, opts, nil).Extract()
		if err != nil {
			return nil, fmt.Errorf("create volume: %w", err)
		}

		vol, err := waitStatus(ctx, kindVolume, created.ID, getVolume(block, created.ID), statusAvailable, statusError)
		if err == nil {
			return vol, nil
		}

		// The volume is of no use, whether it failed or the wait did.
		(&Cleanup{block: block, volumeID: created.ID}).Run(ctx)
		if _, failed := errors.AsType[*failedStatusError](err); !failed || attempt == volumeAttempts {
			return nil, err
		}
		log.Printf("openstack: %v; creating it again", err)
	}
}

// CreateVolumeFromImage creates a restored volume shaped like src.
func (c *Client) CreateVolumeFromImage(ctx context.Context, imageID string, src *volumes.Volume) (*volumes.Volume, error) {
	opts, err := restoreVolumeOpts(src)
	if err != nil {
		return nil, fmt.Errorf("create volume from image: %w", err)
	}
	opts.ImageID = imageID
	return c.createVolume(ctx, opts)
}

// emptyVolume creates a restored volume shaped like src with nothing on it,
// for a restore that writes the disk's bytes to it through an attachment
// instead of going through Glance.
func (c *Client) emptyVolume(ctx context.Context, src *volumes.Volume) (*volumes.Volume, error) {
	opts, err := restoreVolumeOpts(src)
	if err != nil {
		return nil, fmt.Errorf("create empty volume: %w", err)
	}
	return c.createVolume(ctx, opts)
}

// restoreVolumeOpts shapes a restored volume like src: its size, type,
// availability zone, description and metadata, and its name, or
// plakar-restore-<id> if it had none.
func restoreVolumeOpts(src *volumes.Volume) (volumes.CreateOpts, error) {
	if src == nil || src.Size <= 0 {
		return volumes.CreateOpts{}, errors.New("the source volume has no size")
	}
	name := src.Name
	if name == "" {
		name = RestorePrefix + src.ID
	}
	return volumes.CreateOpts{
		Name:             name,
		Size:             src.Size,
		VolumeType:       src.VolumeType,
		AvailabilityZone: src.AvailabilityZone,
		Description:      src.Description,
		Metadata:         src.Metadata,
	}, nil
}

// CreateTempVolumeFromImage creates a volume for reading an image through an
// attachment. The volume is registered with cleanup and given a temporary
// name; unlike CreateVolumeFromImage, it is never a restored result.
func (c *Client) CreateTempVolumeFromImage(ctx context.Context, imageID string, opts volumes.CreateOpts, cleanup *Cleanup) (*volumes.Volume, error) {
	opts.ImageID = imageID
	opts.Name = tmpPrefix + imageID
	tmp, err := c.createVolume(ctx, opts)
	if err != nil {
		return nil, err
	}
	block, err := c.cinder()
	if err != nil {
		c.DeleteTempVolume(ctx, tmp.ID)
		return nil, err
	}
	cleanup.block, cleanup.volumeID = block, tmp.ID
	return tmp, nil
}
