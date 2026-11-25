package gcs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/integration-sdk"
)

type fakeObject struct {
	Name    string `json:"name"`
	Bucket  string `json:"bucket"`
	Size    string `json:"size"`
	Updated string `json:"updated"`
}

// newFakeGCS serves the JSON API object listing and the CONFIG download
// for a single bucket.
func newFakeGCS(t *testing.T, bucket string, objects []fakeObject) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/CONFIG"):
			_, _ = w.Write([]byte("config"))
		case r.URL.Path == "/storage/v1/b/"+bucket+"/o":
			prefix := r.URL.Query().Get("prefix")
			items := []fakeObject{}
			for _, o := range objects {
				if strings.HasPrefix(o.Name, prefix) {
					items = append(items, o)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "storage#objects", "items": items})
		default:
			http.Error(w, fmt.Sprintf("unexpected request %s %s", r.Method, r.URL), http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStorageListFillsSizeAndMTime(t *testing.T) {
	mac := integration.MAC{0xab, 0x01}
	mtime := time.Date(2026, 11, 25, 13, 24, 35, 0, time.UTC)
	srv := newFakeGCS(t, "bucket", []fakeObject{{
		Name:    fmt.Sprintf("repo/packfiles/%02x/%064x", mac[0], mac),
		Bucket:  "bucket",
		Size:    "1234",
		Updated: mtime.Format(time.RFC3339Nano),
	}})

	st, err := NewStore(t.Context(), "gs", map[string]string{
		"location": "gs://bucket/repo",
		"endpoint": srv.URL + "/storage/v1/",
		"no_auth":  "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Open(t.Context()); err != nil {
		t.Fatal(err)
	}

	var got []integration.StorageResourceEntry
	for entry, err := range st.List(t.Context(), integration.StorageResourcePackfile, 0) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, *entry)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].MAC != mac {
		t.Errorf("mac %x, want %x", got[0].MAC, mac)
	}
	if got[0].Size != 1234 {
		t.Errorf("size %d, want 1234", got[0].Size)
	}
	if !got[0].MTime.Equal(mtime) {
		t.Errorf("mtime %v, want %v", got[0].MTime, mtime)
	}
}
