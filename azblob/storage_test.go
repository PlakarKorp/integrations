package azblob

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/integration-sdk"
)

// newFakeBlobService serves the List Blobs operation and the CONFIG
// download for a single container.
func newFakeBlobService(t *testing.T, container, listing string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/CONFIG"):
			_, _ = w.Write([]byte("config"))
		case r.URL.Path == "/"+container && r.URL.Query().Get("comp") == "list":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(listing))
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
	listing := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<EnumerationResults ContainerName="container">
  <Blobs>
    <Blob>
      <Name>repo/packfiles/%02x/%064x</Name>
      <Properties>
        <Last-Modified>%s</Last-Modified>
        <Content-Length>1234</Content-Length>
        <BlobType>BlockBlob</BlobType>
      </Properties>
    </Blob>
  </Blobs>
  <NextMarker />
</EnumerationResults>`, mac[0], mac, mtime.Format(http.TimeFormat))
	srv := newFakeBlobService(t, "container", listing)

	st, err := NewStore(t.Context(), "azblob", map[string]string{
		"location": "azblob://container/repo",
		"endpoint": srv.URL,
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
