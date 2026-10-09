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
	"cmp"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PlakarKorp/integrations/openstack/block"
	"github.com/PlakarKorp/integrations/openstack/common"
	"github.com/PlakarKorp/integrations/openstack/common/keystonetest"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
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

func newExporter(t *testing.T, cloud *keystonetest.Cloud) exporter.Exporter {
	t.Helper()
	exp, err := NewExporter(t.Context(), nil, block.Protocol, params(cloud.AuthURL, "openstack-block://"))
	require.NoError(t, err)
	return exp
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

func metadataRecord(t *testing.T, m common.DiskMetadata) *connectors.Record {
	t.Helper()
	data, err := json.Marshal(m)
	require.NoError(t, err)
	return record(t, "/"+block.MetadataName, data)
}

func diskRecord(t *testing.T, volumeID string) *connectors.Record {
	return record(t, "/"+block.DiskName(volumeID), disk)
}

func rootRecord() *connectors.Record {
	return connectors.NewRecord("/", "", objects.FileInfo{Lname: "/", Lmode: fs.ModeDir | 0700}, nil, nil)
}

func volumeMetadata(diskFormat string) common.DiskMetadata {
	return common.DiskMetadata{
		Origin: common.DiskOriginCinder,
		Image:  &images.Image{DiskFormat: diskFormat},
		Volume: &volumes.Volume{
			ID: "vol-1", Name: "data", Size: 3, VolumeType: "ssd", AvailabilityZone: "nova",
			Description: "orders", Metadata: map[string]string{"team": "db"},
		},
	}
}

// restored returns the one volume left on the cloud, the restored one.
func restored(t *testing.T, cloud *keystonetest.Cloud) string {
	t.Helper()
	left := cloud.Leftovers()
	require.Len(t, left, 1, "only the restored volume is left: %v", left)
	require.True(t, strings.HasPrefix(left[0], "restored-"), "left: %v", left)
	return left[0]
}

func TestExportRestoresVolume(t *testing.T) {
	tests := []struct {
		name          string
		metadata      func() common.DiskMetadata
		savingPolls   int // the uploaded image saves this many reads
		creatingPolls int // the new volume creates this many reads
		wantFormat    string
		wantName      string
	}{
		{name: "raw disk", metadata: func() common.DiskMetadata { return volumeMetadata("raw") }, wantFormat: "raw", wantName: "data"},
		{name: "qcow2 disk", metadata: func() common.DiskMetadata { return volumeMetadata("qcow2") }, wantFormat: "qcow2", wantName: "data"},
		{name: "no recorded format", metadata: func() common.DiskMetadata { return volumeMetadata("") }, wantFormat: "qcow2", wantName: "data"},
		{
			name:          "image and volume take a while",
			metadata:      func() common.DiskMetadata { return volumeMetadata("raw") },
			savingPolls:   2,
			creatingPolls: 2,
			wantFormat:    "raw",
			wantName:      "data",
		},
		{
			name: "no image, unnamed volume",
			metadata: func() common.DiskMetadata {
				m := volumeMetadata("")
				m.Image = nil
				m.Volume.Name = ""
				return m
			},
			wantFormat: "qcow2",
			wantName:   "plakar-restore-vol-1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloud := keystonetest.NewCloud(t)
			cloud.SavingPolls = tt.savingPolls
			cloud.CreatingPolls = tt.creatingPolls
			exp := newExporter(t, cloud)
			m := tt.metadata()

			results, err := exportAll(t.Context(), exp, rootRecord(), metadataRecord(t, m), diskRecord(t, "vol-1"))
			require.NoError(t, err)
			require.Len(t, results, 3, "every record is answered once")
			for _, r := range results {
				assert.NoError(t, r.Err, r.Record.Pathname)
			}

			uploads := cloud.Uploads()
			require.Len(t, uploads, 1)
			assert.Equal(t, tt.wantFormat, uploads[0].DiskFormat)
			assert.Equal(t, "private", uploads[0].Visibility)
			assert.Equal(t, disk, uploads[0].Data)

			requests := cloud.VolumeRequests()
			require.Len(t, requests, 1)
			assert.Equal(t, keystonetest.VolumeRequest{
				ImageID: uploads[0].ID, Name: tt.wantName, Size: 3, VolumeType: "ssd", AvailabilityZone: "nova",
				Description: "orders", Metadata: map[string]string{"team": "db"},
			}, requests[0])

			restored(t, cloud) // and the restore image is gone
			require.NoError(t, exp.Close(t.Context()))
		})
	}
}

// Without the key id, Cinder would wrap the already-encrypted data in a new key.
func TestExportCarriesEncryptionKeyID(t *testing.T) {
	m := volumeMetadata("raw")
	m.ImageProperties = map[string]string{
		"cinder_encryption_key_id":              "key-1",
		"cinder_encryption_key_deletion_policy": "on_image_deletion",
	}
	cloud := keystonetest.NewCloud(t)
	exp := newExporter(t, cloud)

	_, err := exportAll(t.Context(), exp, metadataRecord(t, m), diskRecord(t, "vol-1"))
	require.NoError(t, err)

	uploads := cloud.Uploads()
	require.Len(t, uploads, 1)
	assert.Equal(t, map[string]string{
		"cinder_encryption_key_id":              "key-1",
		"cinder_encryption_key_deletion_policy": "on_image_deletion",
	}, uploads[0].Properties)
	restored(t, cloud)
}

func TestExportRetriesVolumeInError(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.VolumeErrors = 1
	exp := newExporter(t, cloud)

	_, err := exportAll(t.Context(), exp, metadataRecord(t, volumeMetadata("raw")), diskRecord(t, "vol-1"))
	require.NoError(t, err)
	assert.Len(t, cloud.VolumeRequests(), 2)
	restored(t, cloud) // the volume in error is gone
}

func TestExportSkipsUnsupportedPaths(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	exp := newExporter(t, cloud)

	results, err := exportAll(t.Context(), exp, metadataRecord(t, volumeMetadata("raw")), record(t, "/notes.txt", nil), diskRecord(t, "vol-1"))
	require.NoError(t, err)
	require.Len(t, results, 3)
	assert.ErrorContains(t, results[1].Err, `unsupported path "/notes.txt"`)
	restored(t, cloud)
}

// Each case fails the restore; nothing may be left once Close has run.
func TestExportFails(t *testing.T) {
	noVolume := volumeMetadata("raw")
	noVolume.Volume = nil
	noSize := volumeMetadata("raw")
	noSize.Volume.Size = 0

	tests := []struct {
		name          string
		failImageData bool          // the image's data upload fails
		imageStatus   string        // status the uploaded image ends in
		volumeErrors  int           // volumes created from an image end in error
		savingPolls   int           // the uploaded image saves this many reads
		creatingPolls int           // the new volume creates this many reads
		timeout       time.Duration // abort Export after this long; 10s if unset
		records       func(t *testing.T) []*connectors.Record
		wantErr       string
		wantRequests  int // volume create requests
	}{
		{
			name: "disk before metadata",
			records: func(t *testing.T) []*connectors.Record {
				return []*connectors.Record{diskRecord(t, "vol-1"), metadataRecord(t, volumeMetadata("raw"))}
			},
			wantErr: "before",
		},
		{
			name: "disk of another volume",
			records: func(t *testing.T) []*connectors.Record {
				return []*connectors.Record{metadataRecord(t, volumeMetadata("raw")), diskRecord(t, "vol-2")}
			},
			wantErr: "does not match",
		},
		{
			name: "two metadata files",
			records: func(t *testing.T) []*connectors.Record {
				return []*connectors.Record{metadataRecord(t, volumeMetadata("raw")), metadataRecord(t, volumeMetadata("raw"))}
			},
			wantErr: "duplicate",
		},
		{
			name:    "metadata without a volume",
			records: func(t *testing.T) []*connectors.Record { return []*connectors.Record{metadataRecord(t, noVolume)} },
			wantErr: "no volume",
		},
		{
			name:    "volume without a size",
			records: func(t *testing.T) []*connectors.Record { return []*connectors.Record{metadataRecord(t, noSize)} },
			wantErr: "has no size",
		},
		{
			name: "no disk",
			records: func(t *testing.T) []*connectors.Record {
				return []*connectors.Record{metadataRecord(t, volumeMetadata("raw"))}
			},
			wantErr: "no disk",
		},
		{
			// The first disk's restore image is left for Close to delete.
			name: "two disks",
			records: func(t *testing.T) []*connectors.Record {
				return []*connectors.Record{metadataRecord(t, volumeMetadata("raw")), diskRecord(t, "vol-1"), diskRecord(t, "vol-1")}
			},
			wantErr: "a second disk",
		},
		{
			// UploadTempImage deletes the image it could not fill.
			name:          "upload fails",
			failImageData: true,
			records: func(t *testing.T) []*connectors.Record {
				return []*connectors.Record{metadataRecord(t, volumeMetadata("raw")), diskRecord(t, "vol-1")}
			},
			wantErr: "upload image",
		},
		{
			// Every volume is deleted as it fails; Close deletes the image.
			name:         "volume keeps failing",
			volumeErrors: 3,
			records: func(t *testing.T) []*connectors.Record {
				return []*connectors.Record{metadataRecord(t, volumeMetadata("raw")), diskRecord(t, "vol-1")}
			},
			wantErr:      "entered error",
			wantRequests: 3,
		},
		{
			// UploadTempImage deletes the image Glance failed.
			name:        "image killed after upload",
			imageStatus: "killed",
			records: func(t *testing.T) []*connectors.Record {
				return []*connectors.Record{metadataRecord(t, volumeMetadata("raw")), diskRecord(t, "vol-1")}
			},
			wantErr: "entered killed",
		},
		{
			// UploadTempImage deletes the image Glance is still saving.
			name:        "cancelled while the image saves",
			savingPolls: 3,
			timeout:     100 * time.Millisecond,
			records: func(t *testing.T) []*connectors.Record {
				return []*connectors.Record{metadataRecord(t, volumeMetadata("raw")), diskRecord(t, "vol-1")}
			},
			wantErr: "wait for image",
		},
		{
			// The volume still creating is deleted once Cinder allows it;
			// Close deletes the image.
			name:          "cancelled while the volume is created",
			creatingPolls: 3,
			timeout:       100 * time.Millisecond,
			records: func(t *testing.T) []*connectors.Record {
				return []*connectors.Record{metadataRecord(t, volumeMetadata("raw")), diskRecord(t, "vol-1")}
			},
			wantErr:      `restore volume "vol-1": wait for volume`,
			wantRequests: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloud := keystonetest.NewCloud(t)
			cloud.FailImageData = tt.failImageData
			cloud.ImageStatus = tt.imageStatus
			cloud.VolumeErrors = tt.volumeErrors
			cloud.SavingPolls = tt.savingPolls
			cloud.CreatingPolls = tt.creatingPolls
			exp := newExporter(t, cloud)

			ctx, cancel := context.WithTimeout(t.Context(), cmp.Or(tt.timeout, 10*time.Second))
			defer cancel()
			_, err := exportAll(ctx, exp, tt.records(t)...)
			require.ErrorContains(t, err, tt.wantErr)
			if tt.wantRequests > 0 {
				assert.Len(t, cloud.VolumeRequests(), tt.wantRequests)
			}

			require.NoError(t, exp.Close(t.Context()))
			assert.Empty(t, cloud.Leftovers())
		})
	}
}

// Export returns when its context ends, even if records never close.
func TestExportStopsOnCancel(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	exp := newExporter(t, cloud)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	records := make(chan *connectors.Record) // never closed
	results := make(chan *connectors.Result, 1)
	done := make(chan error, 1)
	go func() { done <- exp.Export(ctx, records, results) }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(5 * time.Second):
		t.Fatal("Export did not return once its context ended")
	}
}

func TestPingNeedsCinder(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	exp := newExporter(t, cloud)
	require.NoError(t, exp.Ping(t.Context()))

	cloud.FailVolumeList = true
	assert.Error(t, exp.Ping(t.Context()))
}

func TestNewExporterLocation(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	for _, tt := range []struct {
		location string
		valid    bool
	}{
		{location: "openstack-block://", valid: true},
		{location: "openstack-block://vol-1"},
		{location: "s3://bucket"},
	} {
		t.Run(tt.location, func(t *testing.T) {
			_, err := NewExporter(t.Context(), nil, block.Protocol, params(cloud.AuthURL, tt.location))
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, "location: bad value")
			}
		})
	}
}
