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
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PlakarKorp/integrations/openstack/common"
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

func newExporter(t *testing.T, cloud *keystonetest.Cloud) exporter.Exporter {
	t.Helper()
	exp, err := NewExporter(t.Context(), nil, instance.Protocol, params(cloud.AuthURL, "openstack-instance://"))
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
	return record(nil, dir+"/"+instance.DiskName, disk)
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
		Flavor: &flavors.Flavor{ID: "flavor-1", Name: "m1.small"},
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
		assert.NoError(t, r.Err, r.Record.Pathname)
	}

	// Only the restored volume is left: the temporary restore images, and
	// the Cinder snapshot and temporary volume behind them, are deleted.
	// The spawned server isn't tracked as a Cloud resource.
	left := cloud.Leftovers()
	require.Len(t, left, 1, "left: %v", left)
	assert.True(t, strings.HasPrefix(left[0], "restored-"), "left: %v", left)

	require.NoError(t, exp.Close(t.Context()))
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
		{location: "openstack-instance://server-1", valid: false},
	}
	for _, tt := range tests {
		t.Run(tt.location, func(t *testing.T) {
			_, err := NewExporter(t.Context(), nil, instance.Protocol, params(cloud.AuthURL, tt.location))
			assert.Equal(t, tt.valid, err == nil, "%v", err)
		})
	}
}
