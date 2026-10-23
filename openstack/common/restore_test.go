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
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/PlakarKorp/integrations/openstack/common/keystonetest"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// restoreMetadata is a disk read from a block device, as a backup on the
// cloud records it: raw, with no image behind it.
func restoreMetadata() *DiskMetadata {
	return &DiskMetadata{
		Origin:     DiskOriginCinder,
		DiskFormat: RawDiskFormat,
		Volume: &volumes.Volume{
			ID: "vol-1", Name: "data", Size: 3, VolumeType: "ssd", AvailabilityZone: "nova",
			Description: "orders", Metadata: map[string]string{"team": "db"},
		},
	}
}

func TestValidateWriteAttachment(t *testing.T) {
	qcow2 := restoreMetadata()
	qcow2.DiskFormat = ""
	qcow2.Image = &images.Image{DiskFormat: "qcow2"}
	unrecorded := restoreMetadata()
	unrecorded.DiskFormat = ""
	ciphertext := restoreMetadata()
	ciphertext.ImageProperties = map[string]string{"cinder_encryption_key_id": "key-1"}

	tests := []struct {
		name     string
		metadata *DiskMetadata
		wantErr  string // empty when writable
	}{
		{name: "raw from a device", metadata: restoreMetadata()},
		{name: "qcow2", metadata: qcow2, wantErr: "it is qcow2"},
		{name: "no recorded format", metadata: unrecorded, wantErr: "it is qcow2"},
		{name: "no metadata", metadata: nil, wantErr: "it is qcow2"},
		{name: "raw ciphertext", metadata: ciphertext, wantErr: `encrypted under key "key-1"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWriteAttachment(tt.metadata)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrDiskNotWritable)
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// The restored volume is created blank and shaped like the original,
// attached, written, and detached: it is the one thing left.
func TestAttachRestoreVolumeWritesDevice(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.DetachingPolls = 1
	onCloud(cloud)
	client := connectHost(t, cloud)
	host, err := client.PreflightHost(t.Context())
	require.NoError(t, err)

	w, err := client.AttachRestoreVolume(t.Context(), host, restoreMetadata())
	require.NoError(t, err)
	assert.Equal(t, []string{w.Volume().ID}, cloud.Attached())
	requests := cloud.VolumeRequests()
	require.Len(t, requests, 1)
	assert.Equal(t, keystonetest.VolumeRequest{
		Name: "data", Size: 3, VolumeType: "ssd", AvailabilityZone: "nova",
		Description: "orders", Metadata: map[string]string{"team": "db"},
	}, requests[0])

	data := bytes.Repeat([]byte("restored bytes "), 1<<10)
	vol, err := w.Copy(t.Context(), bytes.NewReader(data))
	require.NoError(t, err)
	assert.Equal(t, w.Volume().ID, vol.ID)
	assert.Equal(t, data, cloud.Written(vol.ID))
	assert.Empty(t, cloud.Attached())
	assert.Equal(t, []string{vol.ID}, cloud.Leftovers(), "the restored volume stays")

	got, err := client.GetVolume(t.Context(), vol.ID)
	require.NoError(t, err)
	assert.Equal(t, "available", got.Status)
}

// A volume Cinder puts in error is created again, as through Glance.
func TestAttachRestoreVolumeRetriesVolumeInError(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.VolumeErrors = 1
	onCloud(cloud)
	client := connectHost(t, cloud)
	host, err := client.PreflightHost(t.Context())
	require.NoError(t, err)

	w, err := client.AttachRestoreVolume(t.Context(), host, restoreMetadata())
	require.NoError(t, err)
	assert.Len(t, cloud.VolumeRequests(), 2)
	_, err = w.Copy(t.Context(), bytes.NewReader([]byte("x")))
	require.NoError(t, err)
	assert.Len(t, cloud.Leftovers(), 1, "the volume in error is gone")
}

// Each refusal comes before any of the disk's bytes are read, with the
// volume, if one was made, deleted again: the caller can go through Glance.
func TestAttachRestoreVolumeRefusals(t *testing.T) {
	qcow2 := restoreMetadata()
	qcow2.DiskFormat = "qcow2"
	ciphertext := restoreMetadata()
	ciphertext.ImageProperties = map[string]string{"cinder_encryption_key_id": "key-1"}

	tests := []struct {
		name             string
		metadata         *DiskMetadata
		failVolumeAttach bool
		deviceReadOnly   bool
		wantErr          error
		wantErrContains  string
		wantRequests     int
	}{
		{name: "qcow2", metadata: qcow2, wantErr: ErrDiskNotWritable},
		{name: "ciphertext", metadata: ciphertext, wantErr: ErrDiskNotWritable},
		{name: "attach refused", metadata: restoreMetadata(), failVolumeAttach: true, wantErrContains: "attach volume", wantRequests: 1},
		{name: "device unwritable", metadata: restoreMetadata(), deviceReadOnly: true, wantErr: os.ErrPermission, wantRequests: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.deviceReadOnly && os.Getuid() == 0 {
				t.Skip("root writes anything")
			}
			cloud := keystonetest.NewCloud(t)
			cloud.FailVolumeAttach = tt.failVolumeAttach
			cloud.DeviceReadOnly = tt.deviceReadOnly
			cloud.DetachingPolls = 1
			onCloud(cloud)
			client := connectHost(t, cloud)
			host, err := client.PreflightHost(t.Context())
			require.NoError(t, err)

			_, err = client.AttachRestoreVolume(t.Context(), host, tt.metadata)
			require.Error(t, err)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			}
			if tt.wantErrContains != "" {
				assert.ErrorContains(t, err, tt.wantErrContains)
			}
			assert.Len(t, cloud.VolumeRequests(), tt.wantRequests)
			assert.Empty(t, cloud.Attached())
			assert.Empty(t, cloud.Leftovers())
		})
	}
}

// failingReader fails after n bytes.
type failingReader struct{ n int }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		return 0, errors.New("read failed")
	}
	n := min(r.n, len(p))
	r.n -= n
	return n, nil
}

// A write that fails leaves a half-written volume, of no use: it is detached
// and deleted.
func TestCopyFailureDeletesVolume(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.DetachingPolls = 1
	onCloud(cloud)
	client := connectHost(t, cloud)
	host, err := client.PreflightHost(t.Context())
	require.NoError(t, err)

	w, err := client.AttachRestoreVolume(t.Context(), host, restoreMetadata())
	require.NoError(t, err)
	_, err = w.Copy(t.Context(), &failingReader{n: 4})
	require.ErrorContains(t, err, "read failed")
	assert.Empty(t, cloud.Attached())
	assert.Empty(t, cloud.Leftovers())
}
