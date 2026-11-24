package storage_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/PlakarKorp/integrations/oci/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uploadRegistry answers just enough of the distribution API for an upload.
// startLoc and patchLoc are the Location headers returned by the upload
// start and the PATCH; "%h" is replaced with the registry's own host. The
// Authorization header of every PATCH is recorded in patchAuth.
func uploadRegistry(t *testing.T, startLoc, patchLoc string, patchAuth *[]string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/repo/blobs/uploads/":
			w.Header().Set("Location", strings.ReplaceAll(startLoc, "%h", srv.Listener.Addr().String()))
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v2/repo/blobs/uploads/"):
			if patchAuth != nil {
				mu.Lock()
				*patchAuth = append(*patchAuth, r.Header.Get("Authorization"))
				mu.Unlock()
			}
			if patchLoc != "" {
				w.Header().Set("Location", strings.ReplaceAll(patchLoc, "%h", srv.Listener.Addr().String()))
			}
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPut:
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

func create(t *testing.T, registryURL string, allowCrossHost bool) error {
	t.Helper()
	ctx := context.Background()
	st, err := storage.New(ctx, "oci", map[string]string{
		"location":                 "oci://" + strings.TrimPrefix(registryURL, "http://") + "/repo",
		"insecure":                 "true",
		"allow_cross_host_uploads": strconv.FormatBool(allowCrossHost),
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
			reg := uploadRegistry(t, startLoc, "/v2/repo/blobs/uploads/abc?_state=x", nil)
			require.NoError(t, create(t, reg.URL, false))
		})
	}
}

// http.Client turns the userinfo of a URL into an Authorization header: a
// registry must not get to pick one through an upload location.
func TestUploadDropsLocationUserinfo(t *testing.T) {
	t.Parallel()
	var patchAuth []string
	reg := uploadRegistry(t, "http://someone:secret@%h/v2/repo/blobs/uploads/abc", "", &patchAuth)
	require.NoError(t, create(t, reg.URL, false))
	require.NotEmpty(t, patchAuth)
	for _, auth := range patchAuth {
		assert.Empty(t, auth)
	}
}

func TestUploadRejectsOffHostStartLocation(t *testing.T) {
	t.Parallel()
	other, hits := elsewhere(t)
	reg := uploadRegistry(t, other.URL+"/upload", "", nil)
	require.Error(t, create(t, reg.URL, false))
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
	for _, allowCrossHost := range []bool{false, true} {
		t.Run("allow_cross_host_uploads="+strconv.FormatBool(allowCrossHost), func(t *testing.T) {
			t.Parallel()
			testUploadRejectsDowngradeToCleartext(t, allowCrossHost)
		})
	}
}

func testUploadRejectsDowngradeToCleartext(t *testing.T, allowCrossHost bool) {
	var cleartext atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Location", "http://"+srv.Listener.Addr().String()+"/v2/repo/blobs/uploads/abc")
		w.WriteHeader(http.StatusAccepted)
	}))
	srv.Listener = cleartextListener{Listener: srv.Listener, cleartext: &cleartext}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	ctx := context.Background()
	st, err := storage.New(ctx, "oci", map[string]string{
		"location":                 "oci://" + srv.Listener.Addr().String() + "/repo",
		"tls_insecure_no_verify":   "true",
		"allow_cross_host_uploads": strconv.FormatBool(allowCrossHost),
	})
	require.NoError(t, err)
	require.Error(t, st.Create(ctx, []byte("config")))
	assert.Zero(t, cleartext.Load(), "registry received cleartext connections")
}

func TestUploadRejectsOffHostPatchLocation(t *testing.T) {
	t.Parallel()
	other, hits := elsewhere(t)
	reg := uploadRegistry(t, "/v2/repo/blobs/uploads/abc", other.URL+"/finish", nil)
	require.Error(t, create(t, reg.URL, false))
	assert.Zero(t, hits.Load(), "off-host server was contacted")
}

func TestUploadFollowsOffHostLocationWhenAllowed(t *testing.T) {
	t.Parallel()
	other, hits := elsewhere(t)
	reg := uploadRegistry(t, other.URL+"/upload", "", nil)
	require.NoError(t, create(t, reg.URL, true))
	assert.NotZero(t, hits.Load(), "off-host server was not contacted")
}

func TestNewRejectsInvalidAllowCrossHostUploads(t *testing.T) {
	t.Parallel()
	_, err := storage.New(context.Background(), "oci", map[string]string{
		"location":                 "oci://localhost/repo",
		"allow_cross_host_uploads": "maybe",
	})
	require.Error(t, err)
}
