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

package keystonetest

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

const Region = "RegionOne"

// Cloud is a Keystone, Cinder and Glance for one project in Region. Any call
// it does not expect, including deleting a resource twice or deleting a
// volume it did not create, fails the test.
type Cloud struct {
	AuthURL string
	// Disk is the content of every image.
	Disk         []byte
	FailUpload   bool
	FailDownload bool
	// FailDeletes is how many deletes fail with a 500, leaving the resource.
	FailDeletes int
	// CreatingPolls is how many reads report a new snapshot or volume as
	// creating.
	CreatingPolls int
	// UploadingPolls is how many reads report a volume uploading to Glance as
	// uploading, and its image as saving.
	UploadingPolls int
	// DeletingPolls is how many reads report a deleted volume as deleting
	// before it is gone.
	DeletingPolls int
	// SnapshotStatus and ImageStatus, when set, are the statuses new
	// snapshots and images end in, instead of available and active.
	SnapshotStatus string
	ImageStatus    string

	t         *testing.T
	mu        sync.Mutex
	next      int
	resources map[string]*resource
	vanished  map[string]bool
}

type resource struct {
	kind      string // volume, snapshot or image
	ours      bool   // created during the test, not given to NewCloud
	source    string // the snapshot a volume was made from, or an image's volume
	creating  int    // reads left reporting creating
	uploading int    // reads left reporting uploading
	deleting  int    // reads left reporting deleting, once deleted
	deleted   bool
}

// NewCloud serves a cloud holding the given volumes.
func NewCloud(t *testing.T, volumes ...string) *Cloud {
	t.Helper()
	c := &Cloud{
		Disk:      []byte("disk bytes"),
		t:         t,
		resources: map[string]*resource{},
		vanished:  map[string]bool{},
	}
	for _, v := range volumes {
		c.resources[v] = &resource{kind: "volume"}
	}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c.AuthURL = srv.URL + "/identity/v3/"

	block := "/volume/v3/p1"
	mux.HandleFunc("POST /identity/v3/auth/tokens", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Subject-Token", "fake-token")
		c.reply(w, http.StatusCreated, map[string]any{"token": map[string]any{
			"expires_at": "2999-01-01T00:00:00.000000Z",
			"project":    map[string]any{"id": "p1"},
			"catalog": []any{
				catalogEntry("volumev3", srv.URL+block),
				catalogEntry("image", srv.URL+"/image/"),
			},
		}})
	})
	mux.HandleFunc("GET "+block+"/volumes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		status, ok := c.read(id, "volume")
		if !ok {
			c.reply(w, http.StatusNotFound, nil)
			return
		}
		c.reply(w, http.StatusOK, map[string]any{"volume": volume(id, status)})
	})
	mux.HandleFunc("POST "+block+"/volumes", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Volume struct {
				SnapshotID string `json:"snapshot_id"`
			} `json:"volume"`
		}
		c.decode(r, &body)
		id, ok := c.create("volume", "tmp", body.Volume.SnapshotID)
		if !ok {
			c.unexpected(w, r)
			return
		}
		c.reply(w, http.StatusAccepted, map[string]any{"volume": volume(id, "creating")})
	})
	mux.HandleFunc("POST "+block+"/volumes/{id}/action", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		c.decode(r, &body)
		if _, ok := body["os-volume_upload_image"]; !ok {
			c.unexpected(w, r)
			return
		}
		if c.FailUpload {
			c.reply(w, http.StatusBadRequest, nil)
			return
		}
		id, ok := c.create("image", "image", r.PathValue("id"))
		if !ok {
			c.unexpected(w, r)
			return
		}
		c.reply(w, http.StatusAccepted, map[string]any{"os-volume_upload_image": map[string]any{"image_id": id}})
	})
	mux.HandleFunc("DELETE "+block+"/volumes/{id}", c.delete)
	mux.HandleFunc("POST "+block+"/snapshots", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Snapshot struct {
				VolumeID string `json:"volume_id"`
			} `json:"snapshot"`
		}
		c.decode(r, &body)
		id, ok := c.create("snapshot", "snapshot", body.Snapshot.VolumeID)
		if !ok {
			c.unexpected(w, r)
			return
		}
		c.reply(w, http.StatusAccepted, map[string]any{"snapshot": snapshot(id, body.Snapshot.VolumeID, "creating")})
	})
	mux.HandleFunc("GET "+block+"/snapshots/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		status, ok := c.read(id, "snapshot")
		if !ok {
			c.reply(w, http.StatusNotFound, nil)
			return
		}
		c.reply(w, http.StatusOK, map[string]any{"snapshot": snapshot(id, "", status)})
	})
	mux.HandleFunc("DELETE "+block+"/snapshots/{id}", c.delete)
	// Glance's catalog URL is unversioned, so gophercloud reads its versions.
	mux.HandleFunc("GET /image/{$}", func(w http.ResponseWriter, r *http.Request) {
		c.reply(w, http.StatusMultipleChoices, map[string]any{"versions": []any{
			map[string]any{"id": "v2.16", "status": "CURRENT"},
		}})
	})
	mux.HandleFunc("GET /image/v2/images/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		status, ok := c.read(id, "image")
		if !ok {
			c.reply(w, http.StatusNotFound, nil)
			return
		}
		c.reply(w, http.StatusOK, map[string]any{
			"id": id, "status": status, "disk_format": "qcow2", "container_format": "bare", "size": len(c.Disk),
		})
	})
	mux.HandleFunc("GET /image/v2/images/{id}/file", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := c.read(r.PathValue("id"), "image"); !ok {
			c.unexpected(w, r)
			return
		}
		if c.FailDownload {
			c.reply(w, http.StatusInternalServerError, nil)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(c.Disk)
	})
	mux.HandleFunc("DELETE /image/v2/images/{id}", c.delete)
	mux.HandleFunc("/", c.unexpected)
	return c
}

// Leftovers lists the resources the test created that still exist.
func (c *Cloud) Leftovers() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, id := range slices.Sorted(maps.Keys(c.resources)) {
		if c.resources[id].ours {
			out = append(out, id)
		}
	}
	return out
}

// Vanish deletes resources behind the client's back, as another user would.
// Reading or deleting them then gets a 404.
func (c *Cloud) Vanish(ids ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range ids {
		delete(c.resources, id)
		c.vanished[id] = true
	}
}

func (c *Cloud) create(kind, prefix, source string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if src, ok := c.resources[source]; !ok || src.deleted {
		return "", false
	}
	c.next++
	id := fmt.Sprintf("%s-%d", prefix, c.next)
	r := &resource{kind: kind, ours: true, source: source}
	switch kind {
	case "image":
		c.resources[source].uploading = c.UploadingPolls
	default:
		r.creating = c.CreatingPolls
	}
	c.resources[id] = r
	return id, true
}

// read returns the resource's status, counting down its transient states.
func (c *Cloud) read(id, kind string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.resources[id]
	if !ok || r.kind != kind {
		return "", false
	}
	switch {
	case r.deleted && r.deleting > 0:
		r.deleting--
		return "deleting", true
	case r.deleted:
		delete(c.resources, id)
		return "", false
	case r.creating > 0:
		r.creating--
		return "creating", true
	case r.uploading > 0:
		r.uploading--
		return "uploading", true
	case kind == "image" && c.resources[r.source] != nil && c.resources[r.source].uploading > 0:
		return "saving", true
	case kind == "image":
		return cmp.Or(c.ImageStatus, "active"), true
	case kind == "snapshot":
		return cmp.Or(c.SnapshotStatus, "available"), true
	}
	return "available", true
}

func (c *Cloud) delete(w http.ResponseWriter, r *http.Request) {
	switch status := c.applyDelete(r.PathValue("id")); status {
	case 0:
		c.unexpected(w, r)
	case http.StatusAccepted:
		w.WriteHeader(status)
	default:
		c.reply(w, status, nil)
	}
}

// applyDelete returns the status of a delete, or 0 for one the client must not
// make. Like Cinder, it refuses to delete a snapshot or volume that is still
// busy, and a snapshot a volume made from it still depends on, as some backends
// do.
func (c *Cloud) applyDelete(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.vanished[id] {
		return http.StatusNotFound
	}
	res, ok := c.resources[id]
	switch {
	case !ok || !res.ours || res.deleted:
		return 0
	case res.creating > 0 || res.uploading > 0 || c.dependedOn(id):
		return http.StatusBadRequest
	case c.FailDeletes > 0:
		c.FailDeletes--
		return http.StatusInternalServerError
	}
	res.deleted = true
	if res.kind == "volume" {
		res.deleting = c.DeletingPolls
	}
	if res.deleting == 0 {
		delete(c.resources, id)
	}
	return http.StatusAccepted
}

// dependedOn reports whether a volume made from snapshot id still exists.
func (c *Cloud) dependedOn(id string) bool {
	for _, r := range c.resources {
		if r.kind == "volume" && r.source == id {
			return true
		}
	}
	return false
}

func catalogEntry(kind, url string) map[string]any {
	return map[string]any{"type": kind, "endpoints": []any{
		map[string]any{"interface": "public", "region": Region, "region_id": Region, "url": url},
	}}
}

func volume(id, status string) map[string]any {
	return map[string]any{"id": id, "status": status, "size": 1, "volume_type": "lvmdriver-1", "attachments": []any{}}
}

func snapshot(id, volumeID, status string) map[string]any {
	return map[string]any{"id": id, "volume_id": volumeID, "status": status, "size": 1}
}

func (c *Cloud) decode(r *http.Request, v any) {
	assert.NoError(c.t, json.NewDecoder(r.Body).Decode(v), "%s %s: decode body", r.Method, r.URL.Path)
}

func (c *Cloud) reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		assert.NoError(c.t, json.NewEncoder(w).Encode(body))
	}
}

func (c *Cloud) unexpected(w http.ResponseWriter, r *http.Request) {
	c.t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
	w.WriteHeader(http.StatusTeapot)
}
