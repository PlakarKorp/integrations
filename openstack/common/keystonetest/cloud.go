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
	"io"
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
	// SavingPolls is how many reads report an image that just received its
	// data as saving.
	SavingPolls int
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
	// FailImageData makes uploading an image's data fail with a 500.
	FailImageData bool
	// VolumeErrors is how many volumes created from an image end in error.
	VolumeErrors int
	// FailVolumeList makes listing volumes fail with a 500.
	FailVolumeList bool

	t         *testing.T
	mu        sync.Mutex
	next      int
	resources map[string]*resource
	vanished  map[string]bool
	uploads   []Upload
	requests  []VolumeRequest
	mux       *http.ServeMux
}

// Upload is an image created through Glance, and the data uploaded to it.
type Upload struct {
	ID, Name, DiskFormat, Visibility string
	Data                             []byte
}

// VolumeRequest is a request to create a volume from an image.
type VolumeRequest struct {
	ImageID          string            `json:"imageRef"`
	Name             string            `json:"name"`
	Description      string            `json:"description"`
	VolumeType       string            `json:"volume_type"`
	AvailabilityZone string            `json:"availability_zone"`
	Size             int               `json:"size"`
	Metadata         map[string]string `json:"metadata"`
}

type resource struct {
	kind      string // volume, snapshot or image
	ours      bool   // created during the test, not given to NewCloud
	source    string // the snapshot a volume was made from, or an image's volume
	creating  int    // reads left reporting creating
	uploading int    // reads left reporting uploading
	deleting  int    // reads left reporting deleting, once deleted
	deleted   bool
	queued    bool   // an image created through Glance, waiting for its data
	format    string // an image's disk_format, when not qcow2
	final     string // the status a volume ends in, when not available
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
				catalogEntry("compute", srv.URL+"/compute/"),
				catalogEntry("network", srv.URL+"/network/"),
			},
		}})
	})
	mux.HandleFunc("GET "+block+"/volumes/detail", func(w http.ResponseWriter, r *http.Request) {
		if c.FailVolumeList {
			c.reply(w, http.StatusInternalServerError, nil)
			return
		}
		c.reply(w, http.StatusOK, map[string]any{"volumes": []any{}})
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
				VolumeRequest
			} `json:"volume"`
		}
		c.decode(r, &body)
		var id string
		var ok bool
		if body.Volume.ImageID != "" {
			id, ok = c.createFromImage(body.Volume.VolumeRequest)
		} else {
			id, ok = c.create("volume", "tmp", body.Volume.SnapshotID)
		}
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
	mux.HandleFunc("POST /image/v2/images", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name       string `json:"name"`
			DiskFormat string `json:"disk_format"`
			Visibility string `json:"visibility"`
		}
		c.decode(r, &body)
		id := c.createImage(Upload{Name: body.Name, DiskFormat: body.DiskFormat, Visibility: body.Visibility})
		c.reply(w, http.StatusCreated, map[string]any{
			"id": id, "name": body.Name, "status": "queued", "disk_format": body.DiskFormat, "container_format": "bare",
		})
	})
	mux.HandleFunc("PUT /image/v2/images/{id}/file", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		assert.NoError(c.t, err, "read the image data")
		switch status := c.storeImageData(r.PathValue("id"), data); status {
		case 0:
			c.unexpected(w, r)
		case http.StatusNoContent:
			w.WriteHeader(status)
		default:
			c.reply(w, status, nil)
		}
	})
	mux.HandleFunc("GET /image/v2/images/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		status, ok := c.read(id, "image")
		if !ok {
			c.reply(w, http.StatusNotFound, nil)
			return
		}
		c.reply(w, http.StatusOK, map[string]any{
			"id": id, "status": status, "disk_format": c.imageFormat(id), "container_format": "bare", "size": len(c.Disk),
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
	// Nova and Neutron's catalog URLs are unversioned, like Glance's.
	mux.HandleFunc("GET /compute/{$}", func(w http.ResponseWriter, r *http.Request) {
		c.reply(w, http.StatusMultipleChoices, map[string]any{"versions": []any{
			map[string]any{"id": "v2.1", "status": "CURRENT"},
		}})
	})
	mux.HandleFunc("GET /network/{$}", func(w http.ResponseWriter, r *http.Request) {
		c.reply(w, http.StatusMultipleChoices, map[string]any{"versions": []any{
			map[string]any{"id": "v2.0", "status": "CURRENT"},
		}})
	})
	mux.HandleFunc("/", c.unexpected)
	c.mux = mux
	return c
}

// Overrule registers a handler for pattern (as http.ServeMux expects, e.g.
// "GET /servers/{id}"), checked ahead of Cloud's own routes. For endpoints
// Cloud does not fake, such as Nova and Neutron.
func (c *Cloud) Overrule(pattern string, handler http.HandlerFunc) {
	c.mux.HandleFunc(pattern, handler)
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

// Uploads lists the images created through Glance, in order.
func (c *Cloud) Uploads() []Upload {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.uploads)
}

// VolumeRequests lists the requests to create a volume from an image, in
// order.
func (c *Cloud) VolumeRequests() []VolumeRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.requests)
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

// createImage creates an image through Glance: queued until its data comes.
func (c *Cloud) createImage(u Upload) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	id := fmt.Sprintf("image-%d", c.next)
	c.resources[id] = &resource{kind: "image", ours: true, queued: true, format: u.DiskFormat}
	u.ID = id
	c.uploads = append(c.uploads, u)
	return id
}

// storeImageData returns the status of an image data upload, or 0 for one the
// client must not make: only a queued image takes data.
func (c *Cloud) storeImageData(id string, data []byte) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	res, ok := c.resources[id]
	if !ok || res.kind != "image" || !res.queued {
		return 0
	}
	if c.FailImageData {
		return http.StatusInternalServerError
	}
	res.queued = false
	res.creating = c.SavingPolls
	for i := range c.uploads {
		if c.uploads[i].ID == id {
			c.uploads[i].Data = data
		}
	}
	return http.StatusNoContent
}

func (c *Cloud) imageFormat(id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if res, ok := c.resources[id]; ok && res.format != "" {
		return res.format
	}
	return "qcow2"
}

// createFromImage creates a volume from an active image, ending in error for
// the first VolumeErrors of them.
func (c *Cloud) createFromImage(req VolumeRequest) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	img, ok := c.resources[req.ImageID]
	// Cinder only creates a volume from an active image.
	if !ok || img.kind != "image" || img.queued || img.creating > 0 || img.deleted {
		return "", false
	}
	c.next++
	id := fmt.Sprintf("restored-%d", c.next)
	r := &resource{kind: "volume", ours: true, source: req.ImageID, creating: c.CreatingPolls}
	if c.VolumeErrors > 0 {
		c.VolumeErrors--
		r.final = "error"
	}
	c.resources[id] = r
	c.requests = append(c.requests, req)
	return id, true
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
	case r.creating > 0 && kind == "image":
		r.creating--
		return "saving", true
	case r.creating > 0:
		r.creating--
		return "creating", true
	case r.uploading > 0:
		r.uploading--
		return "uploading", true
	case r.final != "":
		return r.final, true
	case kind == "image" && r.queued:
		return "queued", true
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
	// Glance deletes an image in any state; Cinder refuses a busy resource.
	case res.kind != "image" && (res.creating > 0 || res.uploading > 0), res.kind == "snapshot" && c.dependedOn(id):
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
