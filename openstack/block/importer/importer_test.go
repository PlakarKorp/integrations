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

package importer

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"testing"
	"time"

	"github.com/PlakarKorp/integrations/openstack/block"
	"github.com/PlakarKorp/integrations/openstack/common"
	"github.com/PlakarKorp/integrations/openstack/common/keystonetest"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func params(authURL, location string) map[string]string {
	return map[string]string{
		"location":                                location,
		"openstack_auth_url":                      authURL,
		"openstack_application_credential_id":     "id",
		"openstack_application_credential_secret": "secret",
		"openstack_region":                        keystonetest.Region,
	}
}

func newImporter(t *testing.T, cloud *keystonetest.Cloud) importer.Importer {
	t.Helper()
	imp, err := NewImporter(t.Context(), nil, block.Protocol, params(cloud.AuthURL, "openstack-block://vol-1"))
	require.NoError(t, err)
	return imp
}

func importAll(ctx context.Context, imp importer.Importer) ([]*connectors.Record, error) {
	records := make(chan *connectors.Record)
	errc := make(chan error, 1)
	go func() { errc <- imp.Import(ctx, records, nil) }()
	var out []*connectors.Record
	for r := range records {
		out = append(out, r)
	}
	return out, <-errc
}

// Without FLAG_STREAM, plakar calls Import twice to count records.
func TestImporterDeclaresStreamFlag(t *testing.T) {
	imp := newImporter(t, keystonetest.NewCloud(t, "vol-1"))
	assert.NotZero(t, imp.Flags()&location.FLAG_STREAM)
}

func TestImportBacksUpVolume(t *testing.T) {
	cloud := keystonetest.NewCloud(t, "vol-1")
	imp := newImporter(t, cloud)

	records, err := importAll(t.Context(), imp)
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.Equal(t, "/vol-1.qcow2", records[0].Pathname)
	assert.Equal(t, "/.METADATA.json", records[1].Pathname)

	disk, err := io.ReadAll(records[0].Reader)
	require.NoError(t, err)
	assert.Equal(t, cloud.Disk, disk)
	require.NoError(t, records[0].Close())
	assert.Empty(t, cloud.Leftovers(), "closing the disk deletes the temporary resources")

	var metadata common.DiskMetadata
	require.NoError(t, json.NewDecoder(records[1].Reader).Decode(&metadata))
	assert.Equal(t, common.DiskOriginCinder, metadata.Origin)
	assert.Equal(t, "vol-1", metadata.Volume.ID)
	assert.Equal(t, "qcow2", metadata.Image.DiskFormat)

	require.NoError(t, imp.Close(t.Context()))
}

// A backup creates a snapshot, a temporary volume and an image. Each case makes
// the backup go wrong so that some of them are still on the cloud afterwards;
// Close must delete them, and log those it cannot.
func TestCloseLeavesNothingBehind(t *testing.T) {
	tests := []struct {
		name string

		// Faults injected into the fake cloud.
		failUpload     bool   // the volume's upload to Glance fails
		failDownload   bool   // the image's download fails
		failDeletes    int    // this many deletes fail
		creatingPolls  int    // new snapshots and volumes report creating this many times
		uploadingPolls int    // the volume reports uploading, and the image saving, this many times
		deletingPolls  int    // a deleted volume reports deleting this many times
		snapshotStatus string // status new snapshots end in
		imageStatus    string // status new images end in

		readDisk       bool          // read and close the disk, as plakar does
		importTimeout  time.Duration // abort Import after this long; 10s if unset
		importErr      string        // what Import fails with; empty if it succeeds
		vanish         []string      // resources someone else deletes before Close
		closeCancelled bool          // call Close with a cancelled context
		wantLeft       string        // the resource Close cannot delete, and logs
	}{
		{
			// Plakar never opens the disk, so closing it cleans nothing up.
			name: "disk never read",
		},
		{
			// The disk never opens, so closing it cleans nothing up.
			name:         "download fails",
			failDownload: true,
			readDisk:     true,
		},
		{
			// Import fails with the snapshot and the temporary volume created.
			name:       "upload to image fails",
			failUpload: true,
			importErr:  "upload volume",
		},
		{
			// Closing the disk fails to delete the image, but deletes the rest.
			// Close does not try it again.
			name:        "delete fails",
			failDeletes: 1,
			readDisk:    true,
			wantLeft:    "image-3",
		},
		{
			// Import is aborted while the snapshot is creating, and Cinder
			// refuses to delete it until it is available. Import reads it once,
			// so with 3 it is still creating when Close first looks.
			name:          "snapshot still creating",
			creatingPolls: 3,
			importTimeout: 100 * time.Millisecond,
			importErr:     "context deadline exceeded",
		},
		{
			// Cinder fails the snapshot. Cinder allows deleting it in error.
			name:           "snapshot enters error",
			snapshotStatus: "error",
			importErr:      `snapshot "snapshot-1" entered error`,
		},
		{
			// Glance fails the upload after the image was created.
			name:        "image killed",
			imageStatus: "killed",
			importErr:   "entered killed",
		},
		{
			// Import is aborted while the volume uploads to Glance. Cinder
			// refuses to delete the volume until the upload is over.
			name:           "aborted while uploading",
			uploadingPolls: 3,
			importTimeout:  100 * time.Millisecond,
			importErr:      "context deadline exceeded",
		},
		{
			// The snapshot can only go once the volume made from it is gone,
			// which takes a while after its delete is accepted.
			name:          "volume deletes slowly",
			deletingPolls: 2,
		},
		{
			// Resources already gone are not failures.
			name:   "already gone",
			vanish: []string{"snapshot-1", "image-3"},
		},
		{
			// Plakar closes the importer with its context already cancelled.
			name:           "close with a cancelled context",
			closeCancelled: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })

			cloud := keystonetest.NewCloud(t, "vol-1")
			cloud.FailUpload = tt.failUpload
			cloud.FailDownload = tt.failDownload
			cloud.FailDeletes = tt.failDeletes
			cloud.CreatingPolls = tt.creatingPolls
			cloud.UploadingPolls = tt.uploadingPolls
			cloud.DeletingPolls = tt.deletingPolls
			cloud.SnapshotStatus = tt.snapshotStatus
			cloud.ImageStatus = tt.imageStatus
			imp := newImporter(t, cloud)

			// Bound Import so a wait that never ends fails the case, not the run.
			ctx, cancel := context.WithTimeout(t.Context(), cmp.Or(tt.importTimeout, 10*time.Second))
			defer cancel()

			records, err := importAll(ctx, imp)
			if tt.importErr != "" {
				require.ErrorContains(t, err, tt.importErr)
			} else {
				require.NoError(t, err)
			}
			if tt.readDisk {
				_, _ = io.ReadAll(records[0].Reader)
				_ = records[0].Close()
			}

			// Something must be left, or the case tests nothing.
			require.NotEmpty(t, cloud.Leftovers())

			cloud.Vanish(tt.vanish...)
			closeCtx := t.Context()
			if tt.closeCancelled {
				var cancel context.CancelFunc
				closeCtx, cancel = context.WithCancel(closeCtx)
				cancel()
			}
			require.NoError(t, imp.Close(closeCtx))
			if tt.wantLeft == "" {
				assert.Empty(t, cloud.Leftovers())
				assert.Empty(t, logs.String())
			} else {
				assert.Equal(t, []string{tt.wantLeft}, cloud.Leftovers())
				assert.Contains(t, logs.String(), fmt.Sprintf("delete image %q", tt.wantLeft))
			}
		})
	}
}

func TestPingFindsVolume(t *testing.T) {
	cloud := keystonetest.NewCloud(t, "vol-1")
	require.NoError(t, newImporter(t, cloud).Ping(t.Context()))

	imp, err := NewImporter(t.Context(), nil, block.Protocol, params(cloud.AuthURL, "openstack-block://vol-2"))
	require.NoError(t, err)
	assert.ErrorContains(t, imp.Ping(t.Context()), `"vol-2"`)
}

func TestNewImporterLocation(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	tests := []struct {
		location string
		volumeID string // empty when the location is rejected
	}{
		{location: "openstack-block://384ba87e-a23e-4965-b3be-8f180e8a8625", volumeID: "384ba87e-a23e-4965-b3be-8f180e8a8625"},
		{location: "openstack-block://vol_1", volumeID: "vol_1"},
		{location: "openstack-block://VOL1", volumeID: "VOL1"},
		{location: "openstack-block://"},
		{location: "openstack-block://."},
		{location: "openstack-block://.."},
		{location: "openstack-block://vol-1/x"},
		{location: "openstack-block:// vol-1"},
		{location: "openstack-block://vol%2F1"},
		{location: "openstack-block://vol-1?x=1"},
		{location: "openstack-block:vol-1"},
		{location: "s3://vol-1"},
	}
	for _, tt := range tests {
		t.Run(tt.location, func(t *testing.T) {
			imp, err := NewImporter(t.Context(), nil, block.Protocol, params(cloud.AuthURL, tt.location))
			if tt.volumeID == "" {
				assert.ErrorContains(t, err, "location: bad value")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.volumeID, imp.Origin())
		})
	}
}
