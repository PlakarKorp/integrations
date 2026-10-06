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
	"net/http"
	"testing"

	"github.com/PlakarKorp/integrations/openstack/common/keystonetest"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeJSON sets the Content-Type gophercloud's pagination relies on to tell
// a JSON body from plain text.
func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func TestGetServer(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.Overrule("GET /compute/servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "server-1" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, `{"server":{"id":"server-1","name":"vmtest","status":"ACTIVE"}}`)
	})
	client := connectCloud(t, cloud)

	server, err := client.GetServer(t.Context(), "server-1")
	require.NoError(t, err)
	assert.Equal(t, "server-1", server.ID)
	assert.Equal(t, "vmtest", server.Name)
}

func TestGetServerNotFound(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.Overrule("GET /compute/servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	client := connectCloud(t, cloud)

	_, err := client.GetServer(t.Context(), "server-1")
	require.ErrorContains(t, err, `get server "server-1"`)
}

func TestServerMetadata(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.Overrule("GET /compute/flavors/{id}", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "flavor-1", r.PathValue("id"))
		writeJSON(w, `{"flavor":{"id":"flavor-1","name":"m1.small","vcpus":1,"ram":2048,"disk":20}}`)
	})
	cloud.Overrule("GET /compute/servers/{id}/os-interface", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "server-1", r.PathValue("id"))
		writeJSON(w, `{"interfaceAttachments":[{"net_id":"net-2","port_id":"port-2"},{"net_id":"net-1","port_id":"port-1"}]}`)
	})
	cloud.Overrule("GET /network/v2.0/networks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		writeJSON(w, `{"network":{"id":"`+id+`","name":"net-`+id+`"}}`)
	})
	client := connectCloud(t, cloud)

	server := &servers.Server{
		ID:     "server-1",
		Flavor: map[string]any{"id": "flavor-1"},
	}
	m, err := client.ServerMetadata(t.Context(), server)
	require.NoError(t, err)
	assert.Equal(t, "flavor-1", m.Flavor.ID)
	require.Len(t, m.Networks, 2)
	// Interface order, not response order.
	assert.Equal(t, "net-2", m.Networks[0].ID)
	assert.Equal(t, "net-1", m.Networks[1].ID)
}

func TestServerMetadataNeedsAFlavorID(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	client := connectCloud(t, cloud)

	_, err := client.ServerMetadata(t.Context(), &servers.Server{ID: "server-1"})
	require.ErrorContains(t, err, `server "server-1": flavor id missing`)
}

// fakeCreateImage overrules the createImage action to hand back image-1, and
// seeds it so the existing Glance image-status route serves it.
func fakeCreateImage(cloud *keystonetest.Cloud) {
	cloud.Overrule("POST /compute/servers/{id}/action", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-OpenStack-Nova-API-Version", "2.90")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"image_id":"image-1"}`))
	})
	cloud.SeedImage("image-1")
}

func TestCreateServerImage(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	fakeCreateImage(cloud)
	client := connectCloud(t, cloud)

	server := &servers.Server{ID: "server-1", Image: map[string]any{"id": "orig-image"}}
	var cleanup Cleanup
	img, err := client.CreateServerImage(t.Context(), server, &cleanup)
	require.NoError(t, err)
	assert.Equal(t, "image-1", img.ID)
}

func TestCreateServerImageRefusesBootFromVolume(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	client := connectCloud(t, cloud)

	server := &servers.Server{ID: "server-1"}
	var cleanup Cleanup
	_, err := client.CreateServerImage(t.Context(), server, &cleanup)
	require.ErrorContains(t, err, `server "server-1": boot-from-volume servers are not supported`)
}

// Nova represents a boot-from-volume server's image as "" or {}, not null.
func TestCreateServerImageRefusesEmptyImage(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	client := connectCloud(t, cloud)

	server := &servers.Server{ID: "server-1", Image: map[string]any{}}
	var cleanup Cleanup
	_, err := client.CreateServerImage(t.Context(), server, &cleanup)
	require.ErrorContains(t, err, `server "server-1": boot-from-volume servers are not supported`)
}

func TestCreateServerImageRefusesBlockDeviceMapping(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.BootFromVolume = true
	fakeCreateImage(cloud)
	client := connectCloud(t, cloud)

	server := &servers.Server{ID: "server-1", Image: map[string]any{"id": "orig-image"}}
	var cleanup Cleanup
	_, err := client.CreateServerImage(t.Context(), server, &cleanup)
	require.ErrorContains(t, err, `server "server-1": boot-from-volume servers are not supported`)
}
