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
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/PlakarKorp/integrations/openstack/common/attach"
	"github.com/PlakarKorp/integrations/openstack/common/keystonetest"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const hostID = "host-1"

// connectHost connects to cloud and points the attach package at its
// metadata service and device directory, so the host the tests run on never
// passes for a Nova server by accident. An answer from the metadata
// service, 404 included, is final, so DMI is never consulted.
func connectHost(t *testing.T, cloud *keystonetest.Cloud) *Client {
	t.Helper()
	savedURL, savedDir := attach.MetadataURL, attach.DeviceDir
	attach.MetadataURL, attach.DeviceDir = cloud.MetadataURL, cloud.DeviceDir
	t.Cleanup(func() { attach.MetadataURL, attach.DeviceDir = savedURL, savedDir })
	return connectCloud(t, cloud)
}

// onCloud makes the test host a Nova server of cloud.
func onCloud(cloud *keystonetest.Cloud) {
	cloud.HostID = hostID
	cloud.Overrule("GET /compute/servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server":{"id":"` + hostID + `","status":"ACTIVE"}}`))
	})
}

func sourceVolume(t *testing.T, client *Client) *volumes.Volume {
	t.Helper()
	vol, err := client.GetVolume(t.Context(), "vol-1")
	require.NoError(t, err)
	return vol
}

func readVolume(ctx context.Context, client *Client, vol *volumes.Volume, cleanup *Cleanup) (*Disk, error) {
	host, _ := client.PreflightHost(ctx)
	snap, err := client.SnapshotVolume(ctx, vol.ID, cleanup)
	if err != nil {
		return nil, err
	}
	tmp, err := client.CreateVolumeFromSnapshot(ctx, snap, cleanup)
	if err != nil {
		return nil, err
	}
	return client.ReadCinderDisk(ctx, host, vol, snap, tmp, cleanup)
}

// readAndClose reads the disk and closes it, as plakar does, which runs the
// cleanup.
func readAndClose(t *testing.T, disk *Disk) []byte {
	t.Helper()
	rc, err := disk.Reader()
	require.NoError(t, err)
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	return data
}

// Choosing how to read the volume.

// Off the cloud, the volume goes through a Glance image, quietly: that is
// the usual case.
func TestReadVolumeOffCloud(t *testing.T) {
	cloud := keystonetest.NewCloud(t, "vol-1")
	client := connectHost(t, cloud)

	var cleanup Cleanup
	disk, err := readVolume(t.Context(), client, sourceVolume(t, client), &cleanup)
	require.NoError(t, err)
	assert.Equal(t, DiskOriginCinder, disk.Metadata.Origin)
	assert.Equal(t, "qcow2", disk.Metadata.DiskFormat)
	require.NotNil(t, disk.Metadata.Image)
	assert.Equal(t, "vol-1", disk.Metadata.Volume.ID)
	assert.NotEmpty(t, disk.Metadata.Snapshot.ID)
	assert.Equal(t, []string{"image-3", "snapshot-1", "tmp-2"}, cloud.Leftovers())

	assert.Equal(t, cloud.Disk, readAndClose(t, disk))
	assert.Empty(t, cloud.Leftovers())
}

// On a Nova server of the cloud, the clone is attached to it and read raw,
// with no image made. Closing the reader detaches and deletes everything,
// as closing an image download does.
func TestReadVolumeOnCloud(t *testing.T) {
	cloud := keystonetest.NewCloud(t, "vol-1")
	onCloud(cloud)
	client := connectHost(t, cloud)

	var cleanup Cleanup
	disk, err := readVolume(t.Context(), client, sourceVolume(t, client), &cleanup)
	require.NoError(t, err)
	assert.Equal(t, DiskOriginCinder, disk.Metadata.Origin)
	assert.Equal(t, "raw", disk.Metadata.DiskFormat)
	assert.Nil(t, disk.Metadata.Image)
	assert.Nil(t, disk.Metadata.ImageProperties)
	assert.Equal(t, "vol-1", disk.Metadata.Volume.ID)
	assert.Equal(t, []string{"snapshot-1", "tmp-2"}, cloud.Leftovers())
	assert.Equal(t, []string{"tmp-2"}, cloud.Attached())

	assert.Equal(t, cloud.Disk, readAndClose(t, disk))
	assert.Empty(t, cloud.Attached())
	assert.Empty(t, cloud.Leftovers())
}

// An attach Nova refuses (another availability zone, say) is not the end of
// the backup: the clone goes through Glance instead.
func TestReadVolumeFallsBackToGlance(t *testing.T) {
	cloud := keystonetest.NewCloud(t, "vol-1")
	cloud.FailVolumeAttach = true
	onCloud(cloud)
	client := connectHost(t, cloud)

	var cleanup Cleanup
	disk, err := readVolume(t.Context(), client, sourceVolume(t, client), &cleanup)
	require.NoError(t, err)
	assert.Equal(t, "qcow2", disk.Metadata.DiskFormat)
	require.NotNil(t, disk.Metadata.Image)

	assert.Equal(t, cloud.Disk, readAndClose(t, disk))
	assert.Empty(t, cloud.Leftovers())
}

// A Nova failure in the preflight is not the end of the backup: the clone
// goes through Glance instead.
func TestReadVolumePreflightFailureFallsBack(t *testing.T) {
	cloud := keystonetest.NewCloud(t, "vol-1")
	cloud.HostID = hostID
	cloud.Overrule("GET /compute/servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	client := connectHost(t, cloud)

	var cleanup Cleanup
	disk, err := readVolume(t.Context(), client, sourceVolume(t, client), &cleanup)
	require.NoError(t, err)
	assert.Equal(t, "qcow2", disk.Metadata.DiskFormat)

	readAndClose(t, disk)
	assert.Empty(t, cloud.Leftovers())
}

// An encrypted volume's bytes differ by path: through Glance they are the
// ciphertext under the key the image names; from a device they are in the
// clear, so a restore must not name the key.
func TestReadVolumeEncrypted(t *testing.T) {
	tests := []struct {
		name           string
		onCloud        bool
		wantProperties map[string]string
	}{
		{"through Glance", false, map[string]string{
			"cinder_encryption_key_id":              "key-1",
			"cinder_encryption_key_deletion_policy": "on_image_deletion",
		}},
		{"from a device", true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloud := keystonetest.NewCloud(t, "vol-1")
			cloud.Encrypt("vol-1")
			cloud.EncryptionKeyID = "key-1"
			if tt.onCloud {
				onCloud(cloud)
			}
			client := connectHost(t, cloud)

			var cleanup Cleanup
			disk, err := readVolume(t.Context(), client, sourceVolume(t, client), &cleanup)
			require.NoError(t, err)
			assert.Equal(t, "raw", disk.Metadata.DiskFormat, "Cinder uploads an encrypted volume raw only")
			assert.Equal(t, tt.wantProperties, disk.Metadata.ImageProperties)

			readAndClose(t, disk)
			assert.Empty(t, cloud.Leftovers())
		})
	}
}

// What a backup records before a failure is cleanup's to delete.
func TestReadVolumeFailureLeavesCleanupToRun(t *testing.T) {
	cloud := keystonetest.NewCloud(t, "vol-1")
	cloud.FailUpload = true
	client := connectHost(t, cloud)

	var cleanup Cleanup
	_, err := readVolume(t.Context(), client, sourceVolume(t, client), &cleanup)
	require.ErrorContains(t, err, "upload volume")
	assert.Equal(t, []string{"snapshot-1", "tmp-2"}, cloud.Leftovers())

	cleanup.Run(t.Context())
	assert.Empty(t, cloud.Leftovers())
}

// Attaching the clone, and undoing it.

// When the kernel never shows the device, the volume is detached again, and
// left available for the Glance path; nothing else is cleaned up.
func TestAttachDetachesWhenNoDeviceTurnsUp(t *testing.T) {
	cloud := keystonetest.NewCloud(t, "vol-1")
	cloud.NoDevice = true
	cloud.DetachingPolls = 1
	onCloud(cloud)
	client := connectHost(t, cloud)

	var cleanup Cleanup
	tmp, err := cloneVolume(t.Context(), client, "vol-1", &cleanup)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond) // the device wait's own timeout is a minute
	defer cancel()
	host, err := client.PreflightHost(ctx)
	require.NoError(t, err)
	_, err = client.volumeBlockDevice(ctx, host, tmp, &cleanup, attach.DeviceReadable)
	require.ErrorContains(t, err, "no device for volume")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Empty(t, cloud.Attached())
	assert.Len(t, cloud.Leftovers(), 2, "the snapshot and the clone stay")

	vol, err := client.GetVolume(t.Context(), tmp.ID)
	require.NoError(t, err)
	assert.Equal(t, "available", vol.Status)

	cleanup.Run(t.Context())
	assert.Empty(t, cloud.Leftovers())
}

// A device the process may not read is found out before plakar opens the
// record, and the volume detached for the Glance path.
func TestAttachDetachesWhenDeviceUnreadable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads anything")
	}
	cloud := keystonetest.NewCloud(t, "vol-1")
	cloud.DeviceUnreadable = true
	cloud.DetachingPolls = 1
	onCloud(cloud)
	client := connectHost(t, cloud)

	var cleanup Cleanup
	tmp, err := cloneVolume(t.Context(), client, "vol-1", &cleanup)
	require.NoError(t, err)
	host, err := client.PreflightHost(t.Context())
	require.NoError(t, err)
	_, err = client.volumeBlockDevice(t.Context(), host, tmp, &cleanup, attach.DeviceReadable)
	require.ErrorIs(t, err, os.ErrPermission)
	assert.Empty(t, cloud.Attached())

	cleanup.Run(t.Context())
	assert.Empty(t, cloud.Leftovers())
}
