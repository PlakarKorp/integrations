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
	"context"
	"log"
	"os"
	"testing"
	"time"

	"github.com/PlakarKorp/integrations/openstack/common/keystonetest"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func connectCloud(t *testing.T, cloud *keystonetest.Cloud) *Client {
	t.Helper()
	cfg, err := ParseConnectorConfig(map[string]string{
		"openstack_auth_url":                      cloud.AuthURL,
		"openstack_application_credential_id":     "id",
		"openstack_application_credential_secret": "secret",
		"openstack_region":                        keystonetest.Region,
	})
	require.NoError(t, err)
	clients, err := Connect(t.Context(), cfg)
	require.NoError(t, err)
	return clients[0]
}

// Waiting for Cinder to allow a delete can take far longer than the delete
// request itself, so the wait must not be bound by the request's timeout.
func TestCleanupWaitOutlastsDeleteTimeout(t *testing.T) {
	defer func(d time.Duration) { deleteTimeout = d }(deleteTimeout)
	deleteTimeout = 200 * time.Millisecond

	cloud := keystonetest.NewCloud(t, "vol-1")
	cloud.CreatingPolls = 3 // about two seconds of waiting once Import stops
	client := connectCloud(t, cloud)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	var cleanup Cleanup
	_, err := client.CreateVolumeImage(ctx, "vol-1", &cleanup)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotEmpty(t, cloud.Leftovers())

	cleanup.Run(t.Context())
	assert.Empty(t, cloud.Leftovers())
}

// Cinder rejects any disk format but raw for a volume of an encrypted type.
func TestCreateVolumeImageUsesRawForEncryptedVolume(t *testing.T) {
	cloud := keystonetest.NewCloud(t, "vol-1")
	cloud.Encrypt("vol-1")
	client := connectCloud(t, cloud)

	var cleanup Cleanup
	vi, err := client.CreateVolumeImage(t.Context(), "vol-1", &cleanup)
	require.NoError(t, err)
	assert.Equal(t, "raw", vi.Image.DiskFormat)

	cleanup.Run(t.Context())
	assert.Empty(t, cloud.Leftovers())
}

// An unencrypted volume keeps the default, qcow2.
func TestCreateVolumeImageUsesDefaultForPlainVolume(t *testing.T) {
	cloud := keystonetest.NewCloud(t, "vol-1")
	client := connectCloud(t, cloud)

	var cleanup Cleanup
	vi, err := client.CreateVolumeImage(t.Context(), "vol-1", &cleanup)
	require.NoError(t, err)
	assert.Equal(t, "qcow2", vi.Image.DiskFormat)

	cleanup.Run(t.Context())
}

// A transient error reading a resource's status, such as Glance briefly
// 500ing right after Cinder hands back an image ID, must not abort the wait.
func TestWaitStatusTakesTransientGetErrors(t *testing.T) {
	cloud := keystonetest.NewCloud(t, "vol-1")
	cloud.FailImageReads = 2
	client := connectCloud(t, cloud)

	var cleanup Cleanup
	_, err := client.CreateVolumeImage(t.Context(), "vol-1", &cleanup)
	require.NoError(t, err)

	cleanup.Run(t.Context())
	assert.Empty(t, cloud.Leftovers())
}

// A status read that never recovers still gives up, instead of polling
// forever.
func TestWaitStatusGivesUpOnPersistentGetErrors(t *testing.T) {
	defer func(n int) { maxTransientGetFailures = n }(maxTransientGetFailures)
	maxTransientGetFailures = 2

	cloud := keystonetest.NewCloud(t, "vol-1")
	cloud.FailImageReads = 100
	client := connectCloud(t, cloud)

	var cleanup Cleanup
	_, err := client.CreateVolumeImage(t.Context(), "vol-1", &cleanup)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `get image "image-`)

	cleanup.Run(t.Context())
}

// Cinder rejects a volume smaller than the image's byte size rounded up to
// GiB, so CreateVolumeFromImage must request at least that, even when it
// exceeds the source volume's own nominal size.
func TestCreateVolumeFromImageSize(t *testing.T) {
	tests := []struct {
		name      string
		imageSize int
		srcSize   int
		want      int
	}{
		{"image fits in the source size", 1024, 5, 5},
		{"image exceeds the source size", 2<<30 + 1, 1, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloud := keystonetest.NewCloud(t)
			cloud.ImageSize = tt.imageSize
			client := connectCloud(t, cloud)

			img, err := client.UploadTempImage(t.Context(), "restore-image", bytes.NewReader([]byte("data")), "qcow2", nil)
			require.NoError(t, err)

			_, err = client.CreateVolumeFromImage(t.Context(), img.ID, &volumes.Volume{ID: "src-1", Size: tt.srcSize})
			require.NoError(t, err)

			reqs := cloud.VolumeRequests()
			require.Len(t, reqs, 1)
			assert.Equal(t, tt.want, reqs[0].Size)
		})
	}
}

// Properties pass through to the image unchanged, including nil.
func TestUploadTempImageSetsProperties(t *testing.T) {
	tests := []struct {
		name       string
		properties map[string]string
	}{
		{"no properties", nil},
		{"encryption properties", map[string]string{"cinder_encryption_key_id": "key-1", "cinder_encryption_key_deletion_policy": "on_image_deletion"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloud := keystonetest.NewCloud(t)
			client := connectCloud(t, cloud)

			_, err := client.UploadTempImage(t.Context(), "restore-image", bytes.NewReader([]byte("data")), "qcow2", tt.properties)
			require.NoError(t, err)

			uploads := cloud.Uploads()
			require.Len(t, uploads, 1)
			assert.Equal(t, tt.properties, uploads[0].Properties)
		})
	}
}

func TestRequiredSizeGiB(t *testing.T) {
	const gib = 1 << 30
	tests := []struct {
		name  string
		bytes int64
		want  int
	}{
		{"zero", 0, 0},
		{"exact multiple", 2 * gib, 2},
		{"one byte over", 2*gib + 1, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, requiredSizeGiB(tt.bytes))
		})
	}
}

// A snapshot that never becomes deletable is left behind, and logged.
func TestCleanupLogsWhenWaitGivesUp(t *testing.T) {
	defer func(d time.Duration) { waitTimeout = d }(waitTimeout)
	waitTimeout = 200 * time.Millisecond
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	cloud := keystonetest.NewCloud(t, "vol-1")
	cloud.CreatingPolls = 100
	client := connectCloud(t, cloud)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	var cleanup Cleanup
	_, err := client.CreateVolumeImage(ctx, "vol-1", &cleanup)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	cleanup.Run(t.Context())
	assert.Equal(t, []string{"snapshot-1"}, cloud.Leftovers())
	assert.Contains(t, logs.String(), `delete snapshot "snapshot-1"`)
}
