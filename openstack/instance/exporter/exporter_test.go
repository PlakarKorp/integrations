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

package exporter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PlakarKorp/integrations/openstack/block"
	"github.com/PlakarKorp/integrations/openstack/common"
	"github.com/PlakarKorp/integrations/openstack/common/attach"
	"github.com/PlakarKorp/integrations/openstack/common/keystonetest"
	"github.com/PlakarKorp/integrations/openstack/instance"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var disk = []byte("disk bytes")

func params(authURL, location string) map[string]string {
	return map[string]string{
		"location":                                location,
		"openstack_auth_url":                      authURL,
		"openstack_application_credential_id":     "id",
		"openstack_application_credential_secret": "secret",
		"openstack_region":                        keystonetest.Region,
	}
}

// newExporterAt also points the attach preflight at cloud's metadata service
// and device directory, so the host the tests run on never passes for a Nova
// server by accident. transfer, when given, sets openstack_disk_transfer.
func newExporterAt(t *testing.T, cloud *keystonetest.Cloud, location string, transfer ...string) exporter.Exporter {
	t.Helper()
	savedURL, savedDir := attach.MetadataURL, attach.DeviceDir
	attach.MetadataURL, attach.DeviceDir = cloud.MetadataURL, cloud.DeviceDir
	t.Cleanup(func() { attach.MetadataURL, attach.DeviceDir = savedURL, savedDir })

	p := params(cloud.AuthURL, location)
	if len(transfer) > 0 {
		p["openstack_disk_transfer"] = transfer[0]
	}
	exp, err := NewExporter(t.Context(), nil, instance.Protocol, p)
	require.NoError(t, err)
	return exp
}

func newExporter(t *testing.T, cloud *keystonetest.Cloud) exporter.Exporter {
	t.Helper()
	return newExporterAt(t, cloud, "openstack-instance://")
}

// hostID is the Nova server the test host pretends to be, on the cloud.
const hostID = "host-1"

// onCloud makes the test host a Nova server of cloud. Nova must still know
// it: fakeSpawn knows every server, fakeExistingServer those it is given.
func onCloud(cloud *keystonetest.Cloud) {
	cloud.HostID = hostID
}

// exportAll feeds recs to Export as plakar does, and collects the results.
func exportAll(ctx context.Context, exp exporter.Exporter, recs ...*connectors.Record) ([]*connectors.Result, error) {
	records := make(chan *connectors.Record)
	results := make(chan *connectors.Result)
	go func() {
		defer close(records)
		for _, r := range recs {
			select {
			case records <- r:
			case <-ctx.Done():
				return
			}
		}
	}()
	collected := make(chan []*connectors.Result)
	go func() {
		var out []*connectors.Result
		for r := range results {
			out = append(out, r)
		}
		collected <- out
	}()
	err := exp.Export(ctx, records, results)
	return <-collected, err
}

// closeOnce fails the test when closed twice: the SDK's record reader panics.
type closeOnce struct {
	io.Reader
	t      *testing.T
	closed atomic.Bool
}

func (r *closeOnce) Close() error {
	if r.closed.Swap(true) {
		r.t.Error("record reader closed twice")
	}
	return nil
}

func record(t *testing.T, path string, data []byte) *connectors.Record {
	name := strings.TrimPrefix(path, "/")
	return connectors.NewRecord(path, "", objects.FileInfo{Lname: name, Lmode: 0600, LmodTime: time.Unix(0, 0)}, nil,
		func() (io.ReadCloser, error) { return &closeOnce{Reader: bytes.NewReader(data), t: t}, nil })
}

func dirRecord(path string) *connectors.Record {
	name := path[strings.LastIndex(path, "/")+1:]
	return connectors.NewRecord(path, "", objects.FileInfo{Lname: name, Lmode: fs.ModeDir | 0700}, nil, nil)
}

func rootRecord() *connectors.Record {
	return connectors.NewRecord("/", "", objects.FileInfo{Lname: "/", Lmode: fs.ModeDir | 0700}, nil, nil)
}

func serverMetadataRecord(t *testing.T, m common.ServerMetadata) *connectors.Record {
	t.Helper()
	data, err := json.Marshal(m)
	require.NoError(t, err)
	return record(t, "/"+instance.MetadataName, data)
}

func diskMetadataRecord(t *testing.T, dir string, m common.DiskMetadata) *connectors.Record {
	t.Helper()
	data, err := json.Marshal(m)
	require.NoError(t, err)
	return record(t, dir+"/"+instance.MetadataName, data)
}

func diskRecord(dir string) *connectors.Record {
	return record(nil, dir+"/"+block.DiskName("disk", "qcow2"), disk)
}

func rawDiskRecord(dir string) *connectors.Record {
	return record(nil, dir+"/"+block.DiskName("disk", "raw"), disk)
}

// deviceRootMetadata is a root disk the importer read from a block device,
// on the cloud: raw, with the Glance image it was snapshotted into behind it.
func deviceRootMetadata() common.DiskMetadata {
	m := rootDiskMetadata()
	m.DiskFormat = "raw"
	return m
}

// deviceVolumeMetadata is an attached volume the importer read from a block
// device: raw, with no image behind it.
func deviceVolumeMetadata() common.DiskMetadata {
	m := volumeDiskMetadata()
	m.DiskFormat = "raw"
	m.Image = nil
	return m
}

// restoredVolumes lists the volumes left on the cloud, the restored ones.
func restoredVolumes(t *testing.T, cloud *keystonetest.Cloud) []string {
	t.Helper()
	left := cloud.Leftovers()
	for _, id := range left {
		require.True(t, strings.HasPrefix(id, "restored-"), "left: %v", left)
	}
	return left
}

func serverMetadata() common.ServerMetadata {
	return common.ServerMetadata{
		Server: &servers.Server{
			ID:   "server-1",
			Name: "vmtest",
			SecurityGroups: []map[string]any{
				{"name": "default"},
			},
		},
		Flavor: &flavors.Flavor{ID: "flavor-1", Name: "m1.small", Disk: 10},
		Networks: []*networks.Network{
			{ID: "net-1", Name: "private"},
		},
	}
}

func rootDiskMetadata() common.DiskMetadata {
	return common.DiskMetadata{
		Origin: common.DiskOriginGlance,
		Image:  &images.Image{DiskFormat: "qcow2"},
	}
}

func volumeDiskMetadata() common.DiskMetadata {
	return common.DiskMetadata{
		Origin: common.DiskOriginCinder,
		Image:  &images.Image{DiskFormat: "qcow2"},
		Volume: &volumes.Volume{ID: "vol-1", Name: "data", Size: 3, VolumeType: "ssd"},
	}
}

// fakeSpawn overrules the Nova/Neutron calls a spawn needs beyond what
// keystonetest fakes by default: resolving the flavor and network by ID,
// and the spawned server's status.
func fakeSpawn(cloud *keystonetest.Cloud) {
	cloud.Overrule("GET /compute/flavors/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"flavor":{"id":"flavor-1","name":"m1.small"}}`)
	})
	cloud.Overrule("GET /network/v2.0/networks/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"network":{"id":"net-1","name":"private"}}`)
	})
	cloud.Overrule("GET /compute/servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"server":{"id":"`+r.PathValue("id")+`","status":"ACTIVE"}}`)
	})
}

// fakeExistingServer overrules the Nova GET existing-server mode checks:
// validIDs exist, anything else 404s.
func fakeExistingServer(cloud *keystonetest.Cloud, validIDs ...string) {
	cloud.Overrule("GET /compute/servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !slices.Contains(validIDs, id) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, `{"server":{"id":"`+id+`","status":"ACTIVE"}}`)
	})
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func TestExportSpawnsServer(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	fakeSpawn(cloud)
	exp := newExporter(t, cloud)

	results, err := exportAll(t.Context(), exp,
		rootRecord(),
		serverMetadataRecord(t, serverMetadata()),
		dirRecord("/block-storage"),
		dirRecord("/block-storage/glance-orig-image"),
		diskMetadataRecord(t, "/block-storage/glance-orig-image", rootDiskMetadata()),
		diskRecord("/block-storage/glance-orig-image"),
		dirRecord("/block-storage/cinder-vol-1"),
		diskMetadataRecord(t, "/block-storage/cinder-vol-1", volumeDiskMetadata()),
		diskRecord("/block-storage/cinder-vol-1"),
	)
	require.NoError(t, err)
	for _, r := range results {
		require.NoError(t, r.Err, r.Record.Pathname)
	}

	// Only the restored volume is left: the temporary restore images, and
	// the Cinder snapshot and temporary volume behind them, are deleted.
	// The spawned server isn't tracked as a Cloud resource.
	left := cloud.Leftovers()
	require.Len(t, left, 1, "left: %v", left)
	assert.True(t, strings.HasPrefix(left[0], "restored-"), "left: %v", left)

	require.NoError(t, exp.Close(t.Context()))
}

// A nil network entry in the server metadata must error, not panic.
func TestExportRejectsNilNetwork(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	fakeSpawn(cloud)
	exp := newExporter(t, cloud)

	metadata := serverMetadata()
	metadata.Networks = append(metadata.Networks, nil)

	_, err := exportAll(t.Context(), exp,
		rootRecord(),
		serverMetadataRecord(t, metadata),
		dirRecord("/block-storage"),
		dirRecord("/block-storage/glance-orig-image"),
		diskMetadataRecord(t, "/block-storage/glance-orig-image", rootDiskMetadata()),
		diskRecord("/block-storage/glance-orig-image"),
	)
	require.ErrorContains(t, err, "nil network")
}

// A restored volume that fails to attach must not be left behind.
func TestExportCleansUpVolumeOnAttachFailure(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.FailVolumeAttach = true
	fakeSpawn(cloud)
	exp := newExporter(t, cloud)

	_, err := exportAll(t.Context(), exp,
		rootRecord(),
		serverMetadataRecord(t, serverMetadata()),
		dirRecord("/block-storage"),
		dirRecord("/block-storage/glance-orig-image"),
		diskMetadataRecord(t, "/block-storage/glance-orig-image", rootDiskMetadata()),
		diskRecord("/block-storage/glance-orig-image"),
		dirRecord("/block-storage/cinder-vol-1"),
		diskMetadataRecord(t, "/block-storage/cinder-vol-1", volumeDiskMetadata()),
		diskRecord("/block-storage/cinder-vol-1"),
	)
	require.ErrorContains(t, err, "attach volume")
	assert.Empty(t, cloud.Leftovers())
}

func TestExportAttachesAllDisksToExistingServer(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	fakeExistingServer(cloud, "server-1")
	exp := newExporterAt(t, cloud, "openstack-instance://server-1")

	results, err := exportAll(t.Context(), exp,
		rootRecord(),
		serverMetadataRecord(t, serverMetadata()),
		dirRecord("/block-storage"),
		dirRecord("/block-storage/glance-orig-image"),
		diskMetadataRecord(t, "/block-storage/glance-orig-image", rootDiskMetadata()),
		diskRecord("/block-storage/glance-orig-image"),
		dirRecord("/block-storage/cinder-vol-1"),
		diskMetadataRecord(t, "/block-storage/cinder-vol-1", volumeDiskMetadata()),
		diskRecord("/block-storage/cinder-vol-1"),
	)
	require.NoError(t, err)
	for _, r := range results {
		require.NoError(t, r.Err, r.Record.Pathname)
	}

	assert.Zero(t, cloud.ServersCreated())

	reqs := cloud.VolumeRequests()
	require.Len(t, reqs, 2)
	assert.Equal(t, serverMetadata().Flavor.Disk, reqs[0].Size, "root disk volume size")
	assert.Equal(t, 3, reqs[1].Size, "data disk volume size")

	require.NoError(t, exp.Close(t.Context()))
}

func TestExportRejectsMissingExistingServer(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	fakeExistingServer(cloud, "server-1")
	exp := newExporterAt(t, cloud, "openstack-instance://server-404")

	require.ErrorContains(t, exp.Ping(t.Context()), "get server")

	_, err := exportAll(t.Context(), exp,
		rootRecord(),
		serverMetadataRecord(t, serverMetadata()),
		dirRecord("/block-storage"),
		dirRecord("/block-storage/glance-orig-image"),
		diskMetadataRecord(t, "/block-storage/glance-orig-image", rootDiskMetadata()),
		diskRecord("/block-storage/glance-orig-image"),
	)
	require.ErrorContains(t, err, "get server")
}

// A disk attached to an existing server that fails to attach must not be
// left behind: same guarantee as spawn mode, in existing-server mode.
func TestExportCleansUpVolumeOnAttachFailureExistingMode(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.FailVolumeAttach = true
	fakeExistingServer(cloud, "server-1")
	exp := newExporterAt(t, cloud, "openstack-instance://server-1")

	_, err := exportAll(t.Context(), exp,
		rootRecord(),
		serverMetadataRecord(t, serverMetadata()),
		dirRecord("/block-storage"),
		dirRecord("/block-storage/glance-orig-image"),
		diskMetadataRecord(t, "/block-storage/glance-orig-image", rootDiskMetadata()),
		diskRecord("/block-storage/glance-orig-image"),
	)
	require.ErrorContains(t, err, "attach volume")
	assert.Empty(t, cloud.Leftovers())
}

// On a Nova server of the cloud, a raw data volume is written to a new volume
// attached to it, then attached to the spawned server: no image is made for
// it. The root disk still goes through Glance, raw as it is: Nova boots the
// new server from an image.
func TestExportSpawnWritesDataVolumeOnCloud(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	onCloud(cloud)
	fakeSpawn(cloud)
	exp := newExporter(t, cloud)

	results, err := exportAll(t.Context(), exp,
		rootRecord(),
		serverMetadataRecord(t, serverMetadata()),
		dirRecord("/block-storage"),
		dirRecord("/block-storage/cinder-vol-1"),
		diskMetadataRecord(t, "/block-storage/cinder-vol-1", deviceVolumeMetadata()),
		rawDiskRecord("/block-storage/cinder-vol-1"),
		dirRecord("/block-storage/glance-orig-image"),
		diskMetadataRecord(t, "/block-storage/glance-orig-image", deviceRootMetadata()),
		rawDiskRecord("/block-storage/glance-orig-image"),
	)
	require.NoError(t, err)
	for _, r := range results {
		require.NoError(t, r.Err, r.Record.Pathname)
	}

	uploads := cloud.Uploads()
	require.Len(t, uploads, 1, "only the root disk goes through Glance")
	assert.Equal(t, "raw", uploads[0].DiskFormat)
	assert.Equal(t, disk, uploads[0].Data)

	requests := cloud.VolumeRequests()
	require.Len(t, requests, 1)
	assert.Equal(t, keystonetest.VolumeRequest{Name: "data", Size: 3, VolumeType: "ssd"}, requests[0], "a blank volume shaped like the original")

	left := restoredVolumes(t, cloud)
	require.Len(t, left, 1, "the data volume; the root image is gone")
	assert.Equal(t, disk, cloud.Written(left[0]))
	assert.Equal(t, 1, cloud.ServersCreated())
	assert.Equal(t, left, cloud.Attached(), "attached to the spawned server")
	require.NoError(t, exp.Close(t.Context()))
	assert.Len(t, cloud.Leftovers(), 1, "Close leaves the attached volume")
}

// Onto an existing server, every raw disk is written through the host, root
// included: nothing goes through Glance.
func TestExportWritesAllDisksOnCloudToExistingServer(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	onCloud(cloud)
	fakeExistingServer(cloud, "server-1", hostID)
	exp := newExporterAt(t, cloud, "openstack-instance://server-1")

	results, err := exportAll(t.Context(), exp,
		rootRecord(),
		serverMetadataRecord(t, serverMetadata()),
		dirRecord("/block-storage"),
		dirRecord("/block-storage/cinder-vol-1"),
		diskMetadataRecord(t, "/block-storage/cinder-vol-1", deviceVolumeMetadata()),
		rawDiskRecord("/block-storage/cinder-vol-1"),
		dirRecord("/block-storage/glance-orig-image"),
		diskMetadataRecord(t, "/block-storage/glance-orig-image", deviceRootMetadata()),
		rawDiskRecord("/block-storage/glance-orig-image"),
	)
	require.NoError(t, err)
	for _, r := range results {
		require.NoError(t, r.Err, r.Record.Pathname)
	}

	assert.Empty(t, cloud.Uploads(), "no image")
	assert.Zero(t, cloud.ServersCreated())
	requests := cloud.VolumeRequests()
	require.Len(t, requests, 2, "blank volumes, in the disks' order")
	assert.Equal(t, 3, requests[0].Size, "data disk volume size")
	assert.Equal(t, serverMetadata().Flavor.Disk, requests[1].Size, "root disk volume size, from the flavor")

	left := restoredVolumes(t, cloud)
	require.Len(t, left, 2)
	for _, id := range left {
		assert.Equal(t, disk, cloud.Written(id), id)
	}
	assert.Equal(t, left, cloud.Attached(), "attached to the existing server")
	require.NoError(t, exp.Close(t.Context()))
}

// On the cloud, a disk that cannot be written to a block device, or one the
// host cannot take, goes through Glance as it would off it, and its volume
// is attached to the server all the same.
func TestExportOnCloudFallsBackToGlance(t *testing.T) {
	tests := []struct {
		name           string
		metadata       common.DiskMetadata
		deviceReadOnly bool // the device turns up unwritable on the host
		wantFormat     string
		wantRequests   int // volume create requests: the blank one that failed, if tried, then the two images'
	}{
		{name: "qcow2 disk", metadata: volumeDiskMetadata(), wantFormat: "qcow2", wantRequests: 2},
		{name: "device unwritable", metadata: deviceVolumeMetadata(), deviceReadOnly: true, wantFormat: "raw", wantRequests: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.deviceReadOnly && os.Getuid() == 0 {
				t.Skip("root writes anything")
			}
			cloud := keystonetest.NewCloud(t)
			cloud.DeviceReadOnly = tt.deviceReadOnly
			cloud.DetachingPolls = 1
			onCloud(cloud)
			fakeExistingServer(cloud, "server-1", hostID)
			exp := newExporterAt(t, cloud, "openstack-instance://server-1")

			results, err := exportAll(t.Context(), exp,
				rootRecord(),
				serverMetadataRecord(t, serverMetadata()),
				dirRecord("/block-storage"),
				dirRecord("/block-storage/cinder-vol-1"),
				diskMetadataRecord(t, "/block-storage/cinder-vol-1", tt.metadata),
				diskRecord("/block-storage/cinder-vol-1"),
				dirRecord("/block-storage/glance-orig-image"),
				diskMetadataRecord(t, "/block-storage/glance-orig-image", rootDiskMetadata()),
				diskRecord("/block-storage/glance-orig-image"),
			)
			require.NoError(t, err)
			for _, r := range results {
				require.NoError(t, r.Err, r.Record.Pathname)
			}

			uploads := cloud.Uploads()
			require.Len(t, uploads, 2, "the data disk, then the root")
			assert.Equal(t, tt.wantFormat, uploads[0].DiskFormat)
			assert.Equal(t, disk, uploads[0].Data)
			requests := cloud.VolumeRequests()
			require.Len(t, requests, tt.wantRequests)
			assert.NotEmpty(t, requests[len(requests)-1].ImageID, "the volumes are created from the images")

			left := restoredVolumes(t, cloud)
			require.Len(t, left, 2, "the images are gone")
			for _, id := range left {
				assert.Nil(t, cloud.Written(id), id)
			}
			assert.Equal(t, left, cloud.Attached())
			require.NoError(t, exp.Close(t.Context()))
		})
	}
}

// openstack_disk_transfer=glance uploads disks the host could have taken,
// without attaching anything to it.
func TestExportTransferGlanceOnCloud(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	onCloud(cloud)
	fakeExistingServer(cloud, "server-1", hostID)
	exp := newExporterAt(t, cloud, "openstack-instance://server-1", "glance")

	_, err := exportAll(t.Context(), exp,
		rootRecord(),
		serverMetadataRecord(t, serverMetadata()),
		dirRecord("/block-storage"),
		dirRecord("/block-storage/cinder-vol-1"),
		diskMetadataRecord(t, "/block-storage/cinder-vol-1", deviceVolumeMetadata()),
		rawDiskRecord("/block-storage/cinder-vol-1"),
		dirRecord("/block-storage/glance-orig-image"),
		diskMetadataRecord(t, "/block-storage/glance-orig-image", deviceRootMetadata()),
		rawDiskRecord("/block-storage/glance-orig-image"),
	)
	require.NoError(t, err)

	require.Len(t, cloud.Uploads(), 2, "both disks go through Glance")
	requests := cloud.VolumeRequests()
	require.Len(t, requests, 2, "no blank volume was tried")
	for _, r := range requests {
		assert.NotEmpty(t, r.ImageID, "the volumes are created from the images")
	}
	left := restoredVolumes(t, cloud)
	require.Len(t, left, 2, "the images are gone")
	for _, id := range left {
		assert.Nil(t, cloud.Written(id), id)
	}
	assert.Equal(t, left, cloud.Attached(), "attached to the existing server")
	require.NoError(t, exp.Close(t.Context()))
}

// openstack_disk_transfer=attach writes every disk through an attachment,
// and fails rather than fall back to Glance when it cannot.
func TestExportTransferAttach(t *testing.T) {
	tests := []struct {
		name             string
		metadata         common.DiskMetadata // the data disk's
		onCloud          bool
		failVolumeAttach bool // Nova refuses the attach
		wantErr          string
	}{
		{name: "raw disks on the cloud", metadata: deviceVolumeMetadata(), onCloud: true},
		{name: "off the cloud", metadata: deviceVolumeMetadata(), wantErr: "direct attachment: cannot attach to this host"},
		{name: "attach refused", metadata: deviceVolumeMetadata(), onCloud: true, failVolumeAttach: true, wantErr: `direct attachment of disk "vol-1": attaching to server`},
		{name: "qcow2 disk", metadata: volumeDiskMetadata(), onCloud: true, wantErr: `direct attachment of disk "vol-1": disk cannot be written to a block device: it is qcow2`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloud := keystonetest.NewCloud(t)
			if tt.onCloud {
				onCloud(cloud)
			}
			cloud.FailVolumeAttach = tt.failVolumeAttach
			cloud.DetachingPolls = 1
			fakeExistingServer(cloud, "server-1", hostID)
			exp := newExporterAt(t, cloud, "openstack-instance://server-1", "attach")

			_, err := exportAll(t.Context(), exp,
				rootRecord(),
				serverMetadataRecord(t, serverMetadata()),
				dirRecord("/block-storage"),
				dirRecord("/block-storage/cinder-vol-1"),
				diskMetadataRecord(t, "/block-storage/cinder-vol-1", tt.metadata),
				rawDiskRecord("/block-storage/cinder-vol-1"),
				dirRecord("/block-storage/glance-orig-image"),
				diskMetadataRecord(t, "/block-storage/glance-orig-image", deviceRootMetadata()),
				rawDiskRecord("/block-storage/glance-orig-image"),
			)
			assert.Empty(t, cloud.Uploads(), "nothing goes through Glance")
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Empty(t, cloud.Attached())
				require.NoError(t, exp.Close(t.Context()))
				assert.Empty(t, cloud.Leftovers())
				return
			}
			require.NoError(t, err)
			left := restoredVolumes(t, cloud)
			require.Len(t, left, 2)
			for _, id := range left {
				assert.Equal(t, disk, cloud.Written(id), id)
			}
			assert.Equal(t, left, cloud.Attached(), "attached to the existing server")
			require.NoError(t, exp.Close(t.Context()))
		})
	}
}

// With openstack_disk_transfer=attach, the root disk of a server to spawn
// still goes through Glance: Nova boots the server from an image. The data
// volume is written through the host.
func TestExportTransferAttachSpawnsFromImage(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	onCloud(cloud)
	fakeSpawn(cloud)
	exp := newExporterAt(t, cloud, "openstack-instance://", "attach")

	_, err := exportAll(t.Context(), exp,
		rootRecord(),
		serverMetadataRecord(t, serverMetadata()),
		dirRecord("/block-storage"),
		dirRecord("/block-storage/cinder-vol-1"),
		diskMetadataRecord(t, "/block-storage/cinder-vol-1", deviceVolumeMetadata()),
		rawDiskRecord("/block-storage/cinder-vol-1"),
		dirRecord("/block-storage/glance-orig-image"),
		diskMetadataRecord(t, "/block-storage/glance-orig-image", rootDiskMetadata()),
		diskRecord("/block-storage/glance-orig-image"),
	)
	require.NoError(t, err)

	require.Len(t, cloud.Uploads(), 1, "only the root disk goes through Glance")
	left := restoredVolumes(t, cloud)
	require.Len(t, left, 1, "the data volume; the root image is gone")
	assert.Equal(t, disk, cloud.Written(left[0]))
	assert.Equal(t, 1, cloud.ServersCreated())
	assert.Equal(t, left, cloud.Attached(), "attached to the spawned server")
	require.NoError(t, exp.Close(t.Context()))
}

// Once a disk's bytes are being written there is no falling back: the
// restore fails, and the half-written volume is detached and deleted.
func TestExportWriteFailureLeavesNothing(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	onCloud(cloud)
	fakeSpawn(cloud)
	exp := newExporter(t, cloud)

	bad := connectors.NewRecord("/block-storage/cinder-vol-1/disk.raw", "", objects.FileInfo{Lname: "disk.raw", Lmode: 0600, LmodTime: time.Unix(0, 0)}, nil,
		func() (io.ReadCloser, error) { return io.NopCloser(&failingReader{n: 4}), nil })
	_, err := exportAll(t.Context(), exp,
		rootRecord(),
		serverMetadataRecord(t, serverMetadata()),
		dirRecord("/block-storage"),
		dirRecord("/block-storage/cinder-vol-1"),
		diskMetadataRecord(t, "/block-storage/cinder-vol-1", deviceVolumeMetadata()),
		bad,
	)
	require.ErrorContains(t, err, `write disk "vol-1"`)
	require.ErrorContains(t, err, "read failed")

	assert.Empty(t, cloud.Uploads())
	assert.Len(t, cloud.VolumeRequests(), 1)
	assert.Empty(t, cloud.Attached())
	require.NoError(t, exp.Close(t.Context()))
	assert.Empty(t, cloud.Leftovers())
}

// A volume written before a later step fails is not yet the server's: Close
// deletes it, as it deletes a restore image.
func TestCloseDeletesWrittenVolumeOnFailure(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	onCloud(cloud)
	fakeSpawn(cloud)
	exp := newExporter(t, cloud)

	metadata := serverMetadata()
	metadata.Networks = append(metadata.Networks, nil)
	_, err := exportAll(t.Context(), exp,
		rootRecord(),
		serverMetadataRecord(t, metadata),
		dirRecord("/block-storage"),
		dirRecord("/block-storage/cinder-vol-1"),
		diskMetadataRecord(t, "/block-storage/cinder-vol-1", deviceVolumeMetadata()),
		rawDiskRecord("/block-storage/cinder-vol-1"),
		dirRecord("/block-storage/glance-orig-image"),
		diskMetadataRecord(t, "/block-storage/glance-orig-image", rootDiskMetadata()),
		diskRecord("/block-storage/glance-orig-image"),
	)
	require.ErrorContains(t, err, "nil network")
	assert.Len(t, cloud.Leftovers(), 2, "the written volume and the root image")

	require.NoError(t, exp.Close(t.Context()))
	assert.Empty(t, cloud.Leftovers())
}

// failingReader fails after a few bytes, as a record whose read fails does.
type failingReader struct{ n int }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		return 0, errors.New("read failed")
	}
	n := min(r.n, len(p))
	r.n -= n
	return n, nil
}

func TestExportRequiresServerMetadata(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	exp := newExporter(t, cloud)

	_, err := exportAll(t.Context(), exp, rootRecord())
	require.ErrorContains(t, err, "no server metadata")
}

func TestExportRequiresARootDisk(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	exp := newExporter(t, cloud)

	_, err := exportAll(t.Context(), exp,
		serverMetadataRecord(t, serverMetadata()),
	)
	require.ErrorContains(t, err, "no root disk")
	require.NoError(t, exp.Close(t.Context()))
}

func TestExportRefusesTwoRootDisks(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	exp := newExporter(t, cloud)

	_, err := exportAll(t.Context(), exp,
		serverMetadataRecord(t, serverMetadata()),
		diskMetadataRecord(t, "/block-storage/glance-image-1", rootDiskMetadata()),
		diskRecord("/block-storage/glance-image-1"),
		diskMetadataRecord(t, "/block-storage/glance-image-2", rootDiskMetadata()),
		diskRecord("/block-storage/glance-image-2"),
	)
	require.ErrorContains(t, err, "more than one root disk")
	require.NoError(t, exp.Close(t.Context()))
}

func TestPingNeedsNova(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.Overrule("GET /compute/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"servers":[]}`)
	})
	exp := newExporter(t, cloud)
	assert.NoError(t, exp.Ping(t.Context()))
}

func TestNewExporterLocation(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	tests := []struct {
		location string
		valid    bool
	}{
		{location: "openstack-instance://", valid: true},
		{location: "openstack-instance://spawn", valid: true},
		{location: "openstack-instance://server-1", valid: true},
		{location: "openstack-instance://bad!id", valid: false},
	}
	for _, tt := range tests {
		t.Run(tt.location, func(t *testing.T) {
			_, err := NewExporter(t.Context(), nil, instance.Protocol, params(cloud.AuthURL, tt.location))
			assert.Equal(t, tt.valid, err == nil, "%v", err)
		})
	}
}

func TestDiskPath(t *testing.T) {
	tests := []struct {
		path   string
		origin common.DiskOrigin
		id     string // empty when path is not a disk path
		name   string
	}{
		{path: "/block-storage/glance-384ba87e-a23e-4965-b3be-8f180e8a8625/disk.qcow2", origin: common.DiskOriginGlance, id: "384ba87e-a23e-4965-b3be-8f180e8a8625", name: "disk.qcow2"},
		{path: "/block-storage/cinder-vol_1/" + instance.MetadataName, origin: common.DiskOriginCinder, id: "vol_1", name: instance.MetadataName},
		{path: "/block-storage/glance-../../etc/passwd/disk.qcow2"},
		{path: "/block-storage/nova-vol-1/disk.qcow2"},
		{path: "/block-storage/glance-/disk.qcow2"},
		{path: "/block-storage/glance-vol-1"},
		{path: "/" + instance.MetadataName},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			origin, id, name, ok := diskPath(tt.path)
			assert.Equal(t, tt.id != "", ok)
			assert.Equal(t, tt.origin, origin)
			assert.Equal(t, tt.id, id)
			assert.Equal(t, tt.name, name)
		})
	}
}
