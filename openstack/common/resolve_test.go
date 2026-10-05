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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveFlavorByID(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.Overrule("GET /compute/flavors/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"flavor":{"id":"flavor-1","name":"m1.small","vcpus":1,"ram":2048,"disk":20}}`)
	})
	client := connectCloud(t, cloud)

	flavor, err := client.ResolveFlavor(t.Context(), "flavor-1", "m1.small")
	require.NoError(t, err)
	assert.Equal(t, "flavor-1", flavor.ID)
}

func TestResolveFlavorFallsBackToName(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.Overrule("GET /compute/flavors/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	cloud.Overrule("GET /compute/flavors/detail", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"flavors":[
			{"id":"flavor-1","name":"m1.small","vcpus":1,"ram":2048,"disk":20},
			{"id":"flavor-2","name":"m1.medium","vcpus":2,"ram":4096,"disk":40}
		]}`)
	})
	client := connectCloud(t, cloud)

	flavor, err := client.ResolveFlavor(t.Context(), "gone-flavor", "m1.medium")
	require.NoError(t, err)
	assert.Equal(t, "flavor-2", flavor.ID)
}

func TestResolveFlavorNameNotFound(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.Overrule("GET /compute/flavors/detail", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"flavors":[]}`)
	})
	client := connectCloud(t, cloud)

	_, err := client.ResolveFlavor(t.Context(), "", "m1.small")
	require.ErrorContains(t, err, `flavor "m1.small": not found`)
}

func TestResolveFlavorNameMultipleMatches(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.Overrule("GET /compute/flavors/detail", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"flavors":[
			{"id":"flavor-1","name":"m1.small","vcpus":1,"ram":2048,"disk":20},
			{"id":"flavor-2","name":"m1.small","vcpus":1,"ram":2048,"disk":20}
		]}`)
	})
	client := connectCloud(t, cloud)

	_, err := client.ResolveFlavor(t.Context(), "", "m1.small")
	require.ErrorContains(t, err, `flavor "m1.small": multiple matches`)
}

func TestResolveNetworkByID(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.Overrule("GET /network/v2.0/networks/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"network":{"id":"net-1","name":"private"}}`)
	})
	client := connectCloud(t, cloud)

	network, err := client.ResolveNetwork(t.Context(), "net-1", "private")
	require.NoError(t, err)
	assert.Equal(t, "net-1", network.ID)
}

func TestResolveNetworkFallsBackToName(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.Overrule("GET /network/v2.0/networks/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	cloud.Overrule("GET /network/v2.0/networks", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "private", r.URL.Query().Get("name"))
		writeJSON(w, `{"networks":[{"id":"net-2","name":"private"}]}`)
	})
	client := connectCloud(t, cloud)

	network, err := client.ResolveNetwork(t.Context(), "gone-net", "private")
	require.NoError(t, err)
	assert.Equal(t, "net-2", network.ID)
}

func TestResolveNetworkNameNotFound(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.Overrule("GET /network/v2.0/networks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"networks":[]}`)
	})
	client := connectCloud(t, cloud)

	_, err := client.ResolveNetwork(t.Context(), "", "private")
	require.ErrorContains(t, err, `network "private": not found`)
}

func TestResolveNetworkNameMultipleMatches(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.Overrule("GET /network/v2.0/networks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"networks":[{"id":"net-1","name":"private"},{"id":"net-2","name":"private"}]}`)
	})
	client := connectCloud(t, cloud)

	_, err := client.ResolveNetwork(t.Context(), "", "private")
	require.ErrorContains(t, err, `network "private": multiple matches`)
}
