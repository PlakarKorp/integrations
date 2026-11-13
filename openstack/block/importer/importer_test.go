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
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/PlakarKorp/integrations/openstack/block"
	"github.com/PlakarKorp/integrations/openstack/common"
	"github.com/PlakarKorp/integrations/openstack/common/attach"
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

// newImporter also points the attach preflight at cloud's metadata service
// and device directory, so the host the tests run on never passes for a Nova
// server by accident.
func newImporter(t *testing.T, cloud *keystonetest.Cloud) importer.Importer {
	t.Helper()
	return newImporterTransfer(t, cloud, "")
}

// newImporterTransfer is newImporter with openstack_transfer set; "" leaves
// it unset, for the default.
func newImporterTransfer(t *testing.T, cloud *keystonetest.Cloud, transfer string) importer.Importer {
	t.Helper()
	savedURL, savedDir := attach.MetadataURL, attach.DeviceDir
	attach.MetadataURL, attach.DeviceDir = cloud.MetadataURL, cloud.DeviceDir
	t.Cleanup(func() { attach.MetadataURL, attach.DeviceDir = savedURL, savedDir })

	p := params(cloud.AuthURL, "openstack-block://vol-1")
	if transfer != "" {
		p["openstack_transfer"] = transfer
	}
	imp, err := NewImporter(t.Context(), nil, block.Protocol, p)
	require.NoError(t, err)
	return imp
}

// neverProbed fails the test if the preflight asks the metadata service.
// Call it after newImporter, which points the preflight at the fake cloud's.
func neverProbed(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("openstack_transfer=glance probed the metadata service")
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	saved := attach.MetadataURL
	attach.MetadataURL = srv.URL + "/openstack/latest/meta_data.json"
	t.Cleanup(func() { attach.MetadataURL = saved })
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
	assert.Equal(t, "qcow2", metadata.DiskFormat)
	assert.Equal(t, "qcow2", metadata.Image.DiskFormat)

	require.NoError(t, imp.Close(t.Context()))
}

// Without the key id in metadata, a restore cannot tell Cinder the data is already locked under it.
func TestImportBacksUpEncryptionKeyID(t *testing.T) {
	cloud := keystonetest.NewCloud(t, "vol-1")
	cloud.Encrypt("vol-1")
	cloud.EncryptionKeyID = "key-1"

	imp := newImporter(t, cloud)
	records, err := importAll(t.Context(), imp)
	require.NoError(t, err)
	require.Len(t, records, 2)

	var metadata common.DiskMetadata
	require.NoError(t, json.NewDecoder(records[1].Reader).Decode(&metadata))
	assert.Equal(t, map[string]string{
		"cinder_encryption_key_id":              "key-1",
		"cinder_encryption_key_deletion_policy": "on_image_deletion",
	}, metadata.ImageProperties)

	require.NoError(t, records[0].Close())
	require.NoError(t, imp.Close(t.Context()))

}

// onCloud makes the test host a Nova server of cloud.
func onCloud(cloud *keystonetest.Cloud) {
	cloud.HostID = "host-1"
	cloud.Overrule("GET /compute/servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server":{"id":"host-1","status":"ACTIVE"}}`))
	})
}

// On a Nova server of the backed-up cloud, the backup attaches a clone of
// the volume to it and reads the block device, raw, with no image made. The
// disk keeps its .qcow2 name; the metadata carries the real format.
func TestImportReadsAttachedVolume(t *testing.T) {
	cloud := keystonetest.NewCloud(t, "vol-1")
	onCloud(cloud)
	imp := newImporter(t, cloud)

	records, err := importAll(t.Context(), imp)
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.Equal(t, "/vol-1.raw", records[0].Pathname)
	assert.Equal(t, []string{"snapshot-1", "tmp-2"}, cloud.Leftovers(), "no image")
	assert.Equal(t, []string{"tmp-2"}, cloud.Attached())

	disk, err := io.ReadAll(records[0].Reader)
	require.NoError(t, err)
	assert.Equal(t, cloud.Disk, disk)
	require.NoError(t, records[0].Close())
	assert.Empty(t, cloud.Attached(), "closing the disk detaches the clone")
	assert.Empty(t, cloud.Leftovers())

	var metadata common.DiskMetadata
	require.NoError(t, json.NewDecoder(records[1].Reader).Decode(&metadata))
	assert.Equal(t, common.DiskOriginCinder, metadata.Origin)
	assert.Equal(t, "raw", metadata.DiskFormat)
	assert.Nil(t, metadata.Image)
	assert.Equal(t, "vol-1", metadata.Volume.ID)

	require.NoError(t, imp.Close(t.Context()))
}

// openstack_transfer decides the route, and a route that was asked for and
// cannot be taken fails the backup instead of becoming the other one:
// {on-cloud, off-cloud, attach refused} x {auto, attach, glance}.
func TestImportTransfer(t *testing.T) {
	tests := []struct {
		name         string
		onCloud      bool // the host is a Nova server of the cloud
		refuseAttach bool // Nova refuses the attach, as it does cross-AZ
		transfer     string
		wantDisk     string // the disk record's path; empty when Import fails
		wantErr      string
	}{
		{name: "off-cloud auto", transfer: "auto", wantDisk: "/vol-1.qcow2"},
		{name: "off-cloud attach", transfer: "attach", wantErr: "openstack_transfer=attach: cannot attach to this host: not a Nova server"},
		{name: "off-cloud glance", transfer: "glance", wantDisk: "/vol-1.qcow2"},
		{name: "on-cloud auto", onCloud: true, transfer: "auto", wantDisk: "/vol-1.raw"},
		{name: "on-cloud attach", onCloud: true, transfer: "attach", wantDisk: "/vol-1.raw"},
		{name: "on-cloud glance", onCloud: true, transfer: "glance", wantDisk: "/vol-1.qcow2"},
		{name: "attach refused auto", onCloud: true, refuseAttach: true, transfer: "auto", wantDisk: "/vol-1.qcow2"},
		{name: "attach refused attach", onCloud: true, refuseAttach: true, transfer: "attach", wantErr: `openstack_transfer=attach: volume "vol-1": attaching to server "host-1"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloud := keystonetest.NewCloud(t, "vol-1")
			cloud.FailVolumeAttach = tt.refuseAttach
			if tt.onCloud {
				onCloud(cloud)
			}
			imp := newImporterTransfer(t, cloud, tt.transfer)
			if tt.transfer == "glance" {
				neverProbed(t)
			}

			records, err := importAll(t.Context(), imp)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Empty(t, records)
				if !tt.onCloud {
					assert.Empty(t, cloud.Leftovers(), "the route is decided before the snapshot")
				}
				require.NoError(t, imp.Close(t.Context()))
				assert.Empty(t, cloud.Leftovers())
				return
			}
			require.NoError(t, err)
			require.Len(t, records, 2)
			assert.Equal(t, tt.wantDisk, records[0].Pathname)
			if tt.wantDisk == "/vol-1.qcow2" {
				assert.Empty(t, cloud.Attached())
			}

			disk, err := io.ReadAll(records[0].Reader)
			require.NoError(t, err)
			assert.Equal(t, cloud.Disk, disk)
			require.NoError(t, records[0].Close())
			require.NoError(t, imp.Close(t.Context()))
			assert.Empty(t, cloud.Leftovers())
		})
	}
}

// Close detaches a clone plakar never read.
func TestCloseDetachesUnreadVolume(t *testing.T) {
	cloud := keystonetest.NewCloud(t, "vol-1")
	onCloud(cloud)
	imp := newImporter(t, cloud)

	_, err := importAll(t.Context(), imp)
	require.NoError(t, err)
	require.Equal(t, []string{"tmp-2"}, cloud.Attached())

	require.NoError(t, imp.Close(t.Context()))
	assert.Empty(t, cloud.Attached())
	assert.Empty(t, cloud.Leftovers())
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
			// What the cleanup logs is the claim: Import logged going
			// through Glance, as it does off the cloud under auto.
			logs.Reset()
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
		location    string
		volumeID    string // empty when the location is rejected
		errContains string // required when the location is rejected
	}{
		{location: "openstack-block://384ba87e-a23e-4965-b3be-8f180e8a8625", volumeID: "384ba87e-a23e-4965-b3be-8f180e8a8625"},
		{location: "openstack-block://vol_1", volumeID: "vol_1"},
		{location: "openstack-block://VOL1", volumeID: "VOL1"},
		{location: "openstack-block://", errContains: "location: bad value"},
		{location: "openstack-block://.", errContains: "location: bad value"},
		{location: "openstack-block://..", errContains: "location: bad value"},
		{location: "openstack-block://vol-1/x", errContains: "location: bad value"},
		{location: "openstack-block:// vol-1", errContains: "location: bad value"},
		{location: "openstack-block://vol%2F1", errContains: "location: bad value"},
		{location: "openstack-block://vol-1?x=1", errContains: "location: bad value"},
		{location: "openstack-block:vol-1", errContains: "location: bad value"},
		{location: "s3://vol-1", errContains: "location: bad value"},
		{location: "openstack-block://spawn", errContains: "create-new placeholder"},
	}
	for _, tt := range tests {
		t.Run(tt.location, func(t *testing.T) {
			imp, err := NewImporter(t.Context(), nil, block.Protocol, params(cloud.AuthURL, tt.location))
			if tt.volumeID == "" {
				assert.ErrorContains(t, err, tt.errContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.volumeID, imp.Origin())
		})
	}
}
