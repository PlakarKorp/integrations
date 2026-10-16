package common

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeImportCloud is a Swift and a Glance import endpoint for one project.
// Any call it does not expect fails the test.
type fakeImportCloud struct {
	t *testing.T

	mu         sync.Mutex
	containers map[string]http.Header
	objects    map[string][]byte
	objectHdrs map[string]http.Header
	deleted    map[string]bool
	bulkDelete []string
	imports    []importRequest
}

type importRequest struct {
	imageID, name, uri string
}

func newFakeImportCloud(t *testing.T) (*fakeImportCloud, *gophercloud.ServiceClient, *gophercloud.ServiceClient) {
	t.Helper()
	f := &fakeImportCloud{
		t:          t,
		containers: map[string]http.Header{},
		objects:    map[string][]byte{},
		objectHdrs: map[string]http.Header{},
		deleted:    map[string]bool{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/AUTH_p1/{container}", f.createContainer)
	mux.HandleFunc("DELETE /v1/AUTH_p1/{container}", f.deleteContainer)
	mux.HandleFunc("PUT /v1/AUTH_p1/{container}/{object}", f.createObject)
	mux.HandleFunc("DELETE /v1/AUTH_p1/{container}/{object}", f.deleteObject)
	mux.HandleFunc("POST /v1/AUTH_p1/", f.handleBulkDelete)
	mux.HandleFunc("POST /v2/images/{id}/import", f.handleImport)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	swift := &gophercloud.ServiceClient{ProviderClient: &gophercloud.ProviderClient{}, Endpoint: srv.URL + "/v1/AUTH_p1/"}
	glance := &gophercloud.ServiceClient{ProviderClient: &gophercloud.ProviderClient{}, Endpoint: srv.URL + "/v2/"}
	return f, swift, glance
}

func (f *fakeImportCloud) createContainer(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.containers[r.PathValue("container")] = r.Header.Clone()
	w.WriteHeader(http.StatusCreated)
}

func (f *fakeImportCloud) deleteContainer(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted[r.PathValue("container")] = true
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeImportCloud) createObject(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	require.NoError(f.t, err)

	// Swift's own check: the manifest needs a container and a non-empty prefix.
	if m := r.Header.Get("X-Object-Manifest"); m != "" {
		if container, prefix, ok := strings.Cut(m, "/"); !ok || container == "" || prefix == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	key := r.PathValue("container") + "/" + r.PathValue("object")
	f.objects[key] = body
	f.objectHdrs[key] = r.Header.Clone()
	w.WriteHeader(http.StatusCreated)
}

func (f *fakeImportCloud) deleteObject(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted[r.PathValue("container")+"/"+r.PathValue("object")] = true
	w.WriteHeader(http.StatusNoContent)
}

// handleBulkDelete decodes Swift's bulk-delete body: one "container/object"
// per line, each segment URL-escaped.
func (f *fakeImportCloud) handleBulkDelete(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("bulk-delete") != "true" {
		f.t.Errorf("bulk delete missing ?bulk-delete=true: %s", r.URL)
	}
	body, err := io.ReadAll(r.Body)
	require.NoError(f.t, err)

	f.mu.Lock()
	defer f.mu.Unlock()
	for line := range strings.SplitSeq(strings.TrimSpace(string(body)), "\n") {
		if line == "" {
			continue
		}
		decoded, err := url.PathUnescape(line)
		require.NoError(f.t, err)
		f.bulkDelete = append(f.bulkDelete, decoded)
		f.deleted[decoded] = true
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"Response Status": "200 OK"})
}

func (f *fakeImportCloud) handleImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Method struct {
			Name string `json:"name"`
			URI  string `json:"uri"`
		} `json:"method"`
	}
	require.NoError(f.t, json.NewDecoder(r.Body).Decode(&body))

	f.mu.Lock()
	f.imports = append(f.imports, importRequest{imageID: r.PathValue("id"), name: body.Method.Name, uri: body.Method.URI})
	f.mu.Unlock()
	w.WriteHeader(http.StatusAccepted)
}

func TestUploadSegmentsSplitsAndReassembles(t *testing.T) {
	origSize, origWorkers := segmentSize, segmentWorkers
	segmentSize, segmentWorkers = 4, 2
	t.Cleanup(func() { segmentSize, segmentWorkers = origSize, origWorkers })

	tests := []struct {
		name string
		data string
		n    int
	}{
		{name: "empty", data: "", n: 0},
		{name: "one short segment", data: "ab", n: 1},
		{name: "exact multiple", data: "abcdefgh", n: 2},
		{name: "trailing partial segment", data: "abcdefghij", n: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, swift, _ := newFakeImportCloud(t)

			n, err := uploadSegments(t.Context(), swift, "segs", strings.NewReader(tt.data))
			require.NoError(t, err)
			assert.Equal(t, tt.n, n)

			var got bytes.Buffer
			for i := range n {
				got.Write(f.objects["segs/"+segmentName(i)])
			}
			assert.Equal(t, tt.data, got.String())
		})
	}
}

func TestUploadStagedImageCreatesManifestAndImport(t *testing.T) {
	f, swift, glance := newFakeImportCloud(t)
	_ = swift

	origSize := segmentSize
	segmentSize = 4
	t.Cleanup(func() { segmentSize = origSize })

	cleanup, err := uploadStagedImage(t.Context(), swift, glance, "img-1", strings.NewReader("0123456789"))
	t.Cleanup(func() { cleanup(context.Background()) })
	require.NoError(t, err)

	staging, segments := stagingContainer("img-1"), segmentsContainer("img-1")

	keyHeader := f.containers[staging].Get("X-Container-Meta-Temp-URL-Key")
	require.Len(t, keyHeader, 64, "temp URL key: want 32 random bytes hex-encoded")
	_, segmentsCreated := f.containers[segments]
	assert.True(t, segmentsCreated)

	manifestHdrs := f.objectHdrs[staging+"/"+manifestObject]
	require.NotNil(t, manifestHdrs)
	assert.Equal(t, segments+"/"+segmentPrefix, manifestHdrs.Get("X-Object-Manifest"))
	assert.Empty(t, f.objects[staging+"/"+manifestObject])

	require.Len(t, f.imports, 1)
	assert.Equal(t, "img-1", f.imports[0].imageID)
	assert.Equal(t, "web-download", f.imports[0].name)

	tempURL, err := url.Parse(f.imports[0].uri)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("/v1/AUTH_p1/%s/%s", staging, manifestObject), tempURL.Path)
	assertValidTempURLSignature(t, tempURL, keyHeader)

	cleanup(context.Background())
	for i := range 3 {
		assert.True(t, f.deleted[segments+"/"+segmentName(i)], "segment %d deleted", i)
	}
	assert.True(t, f.deleted[segments], "segments container deleted")
	assert.True(t, f.deleted[staging+"/"+manifestObject], "manifest deleted")
	assert.True(t, f.deleted[staging], "staging container deleted")
}

// assertValidTempURLSignature recomputes Swift's Temp URL HMAC
// (https://docs.openstack.org/swift/latest/api/temporary_url_middleware.html)
// against the key the staging container was created with, rather than
// trusting gophercloud's objects.CreateTempURL output by construction.
func assertValidTempURLSignature(t *testing.T, tempURL *url.URL, key string) {
	t.Helper()
	expires := tempURL.Query().Get("temp_url_expires")
	wantSig := tempURL.Query().Get("temp_url_sig")
	require.NotEmpty(t, expires)
	require.NotEmpty(t, wantSig)

	mac := hmac.New(sha1.New, []byte(key))
	_, _ = fmt.Fprintf(mac, "GET\n%s\n%s", expires, tempURL.Path)
	assert.Equal(t, hex.EncodeToString(mac.Sum(nil)), wantSig)
}
