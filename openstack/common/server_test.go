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

func TestGetServer(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	cloud.Overrule("GET /compute/servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "server-1" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"server":{"id":"server-1","name":"vmtest","status":"ACTIVE"}}`))
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
