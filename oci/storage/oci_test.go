package storage_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PlakarKorp/integrations/oci/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRegistry answers just enough of the distribution API for an upload.
// startLoc and patchLoc are the Location headers returned by the upload
// start and the PATCH; "%h" is replaced with the registry's own host.
func fakeRegistry(t *testing.T, startLoc, patchLoc string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/v2/repo/blobs/uploads/":
			w.Header().Set("Location", strings.ReplaceAll(startLoc, "%h", srv.Listener.Addr().String()))
			w.WriteHeader(http.StatusAccepted)
		case r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, "/v2/repo/blobs/uploads/"):
			if patchLoc != "" {
				w.Header().Set("Location", strings.ReplaceAll(patchLoc, "%h", srv.Listener.Addr().String()))
			}
			w.WriteHeader(http.StatusAccepted)
		case r.Method == "PUT":
			w.WriteHeader(http.StatusCreated)
		default:
			assert.Failf(t, "unexpected request", "%s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// elsewhere is an off-host server that must never be contacted.
func elsewhere(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func create(t *testing.T, registryURL string) error {
	t.Helper()
	ctx := context.Background()
	st, err := storage.New(ctx, "oci", map[string]string{
		"location": "oci://" + strings.TrimPrefix(registryURL, "http://") + "/repo",
		"insecure": "true",
	})
	require.NoError(t, err)
	return st.Create(ctx, []byte("config"))
}

func TestUploadFollowsSameHostLocation(t *testing.T) {
	t.Parallel()
	for name, startLoc := range map[string]string{
		"relative": "/v2/repo/blobs/uploads/abc",
		"absolute": "http://%h/v2/repo/blobs/uploads/abc",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			reg := fakeRegistry(t, startLoc, "/v2/repo/blobs/uploads/abc?_state=x")
			require.NoError(t, create(t, reg.URL))
		})
	}
}

func TestUploadRejectsOffHostStartLocation(t *testing.T) {
	t.Parallel()
	other, hits := elsewhere(t)
	reg := fakeRegistry(t, other.URL+"/upload", "")
	require.Error(t, create(t, reg.URL))
	assert.Zero(t, hits.Load(), "off-host server was contacted")
}

// cleartextListener counts connections whose first byte is not a TLS record.
type cleartextListener struct {
	net.Listener
	cleartext *atomic.Int32
}

func (l cleartextListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &firstByteConn{Conn: c, cleartext: l.cleartext}, nil
}

type firstByteConn struct {
	net.Conn
	cleartext *atomic.Int32
	seen      bool
}

func (c *firstByteConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if !c.seen && n > 0 {
		c.seen = true
		if b[0] != 0x16 {
			c.cleartext.Add(1)
		}
	}
	return n, err
}

func TestUploadRejectsDowngradeToCleartext(t *testing.T) {
	t.Parallel()
	var cleartext atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://"+srv.Listener.Addr().String()+"/v2/repo/blobs/uploads/abc")
		w.WriteHeader(http.StatusAccepted)
	}))
	srv.Listener = cleartextListener{Listener: srv.Listener, cleartext: &cleartext}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	ctx := context.Background()
	st, err := storage.New(ctx, "oci", map[string]string{
		"location":               "oci://" + srv.Listener.Addr().String() + "/repo",
		"tls_insecure_no_verify": "true",
	})
	require.NoError(t, err)
	require.Error(t, st.Create(ctx, []byte("config")))
	assert.Zero(t, cleartext.Load(), "registry received cleartext connections")
}

func TestUploadRejectsOffHostPatchLocation(t *testing.T) {
	t.Parallel()
	other, hits := elsewhere(t)
	reg := fakeRegistry(t, "/v2/repo/blobs/uploads/abc", other.URL+"/finish")
	require.Error(t, create(t, reg.URL))
	assert.Zero(t, hits.Load(), "off-host server was contacted")
}
