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
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/PlakarKorp/integrations/openstack/common/keystonetest"
	"github.com/PlakarKorp/integrations/openstack/instance"
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
	imp, err := NewImporter(t.Context(), nil, instance.Protocol, params(cloud.AuthURL, "openstack-instance://server-1"))
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

// fakeServer answers the compute/network calls Import needs: the server (one
// attached volume, no interfaces), its flavor, and its snapshot image.
func fakeServer(cloud *keystonetest.Cloud) {
	cloud.Overrule("GET /compute/servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"server":{"id":"server-1","flavor":{"id":"flavor-1"},"image":{"id":"orig-image"},`+
			`"os-extended-volumes:volumes_attached":[{"id":"vol-1"}]}}`)
	})
	cloud.Overrule("GET /compute/flavors/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"flavor":{"id":"flavor-1","name":"m1.small","vcpus":1,"ram":2048,"disk":20}}`)
	})
	cloud.Overrule("GET /compute/servers/{id}/os-interface", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"interfaceAttachments":[]}`)
	})
	cloud.Overrule("POST /compute/servers/{id}/action", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-OpenStack-Nova-API-Version", "2.90")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"image_id":"image-1"}`))
	})
	cloud.SeedImage("image-1")
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

// Without FLAG_STREAM, plakar calls Import twice to count records.
func TestImporterDeclaresStreamFlag(t *testing.T) {
	imp := newImporter(t, keystonetest.NewCloud(t, "vol-1"))
	assert.NotZero(t, imp.Flags()&location.FLAG_STREAM)
}

func TestImportBacksUpServer(t *testing.T) {
	cloud := keystonetest.NewCloud(t, "vol-1")
	fakeServer(cloud)
	imp := newImporter(t, cloud)

	records, err := importAll(t.Context(), imp)
	require.NoError(t, err)
	require.Len(t, records, 8)
	assert.Equal(t, "/.METADATA.json", records[0].Pathname)
	assert.Equal(t, "/block-storage", records[1].Pathname)
	assert.True(t, records[1].FileInfo.IsDir())
	assert.Equal(t, "/block-storage/glance-image-1", records[2].Pathname)
	assert.True(t, records[2].FileInfo.IsDir())
	assert.Equal(t, "/block-storage/glance-image-1/.METADATA.json", records[3].Pathname)
	assert.Equal(t, "/block-storage/glance-image-1/disk.qcow2", records[4].Pathname)
	assert.Equal(t, "/block-storage/cinder-vol-1", records[5].Pathname)
	assert.True(t, records[5].FileInfo.IsDir())
	assert.Equal(t, "/block-storage/cinder-vol-1/.METADATA.json", records[6].Pathname)
	assert.Equal(t, "/block-storage/cinder-vol-1/disk.qcow2", records[7].Pathname)
	for _, r := range records {
		assert.NoError(t, r.Err, r.Pathname)
		// Lname must be the bare basename: kloset's vfs rejects a slash in
		// it (https://.../snapshot/vfs/entry.go's Entry.validate).
		assert.NotContains(t, r.FileInfo.Lname, "/", r.Pathname)
		if r.FileInfo.IsDir() {
			continue
		}
		// A disk record's cleanup runs once its reader is opened and
		// closed, as plakar does; metadata records have no cleanup.
		_, readErr := io.ReadAll(r.Reader)
		require.NoError(t, readErr, r.Pathname)
		require.NoError(t, r.Close())
	}
	assert.Empty(t, cloud.Leftovers(), "closing every disk deletes the temporary resources")

	require.NoError(t, imp.Close(t.Context()))
}

func TestImportRefusesBootFromVolume(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.Overrule("GET /compute/servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"server":{"id":"server-1","flavor":{"id":"flavor-1"}}}`)
	})
	cloud.Overrule("GET /compute/flavors/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"flavor":{"id":"flavor-1","name":"m1.small","vcpus":1,"ram":2048,"disk":20}}`)
	})
	cloud.Overrule("GET /compute/servers/{id}/os-interface", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"interfaceAttachments":[]}`)
	})
	imp := newImporter(t, cloud)

	_, err := importAll(t.Context(), imp)
	require.ErrorContains(t, err, "boot-from-volume servers are not supported")
	require.NoError(t, imp.Close(t.Context()))
	assert.Empty(t, cloud.Leftovers())
}

func TestNewImporterLocation(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	tests := []struct {
		location string
		valid    bool
	}{
		{location: "openstack-instance://server-1", valid: true},
		{location: "openstack-instance://", valid: false},
		{location: "openstack-instance://server-1/x", valid: false},
		{location: "openstack-instance://../server-1", valid: false},
	}
	for _, tt := range tests {
		t.Run(tt.location, func(t *testing.T) {
			_, err := NewImporter(t.Context(), nil, instance.Protocol, params(cloud.AuthURL, tt.location))
			assert.Equal(t, tt.valid, err == nil, "%v", err)
		})
	}
}
