package xoa_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/integration-xcp-ng/xcp-ng/xoa"
	"github.com/stretchr/testify/require"
)

func newClient(t *testing.T, srv *httptest.Server, base string) *xoa.Client {
	t.Helper()
	c, err := xoa.New(xoa.Config{URL: srv.URL + base, Token: "secret", InsecureSkipVerify: true})
	require.NoError(t, err)
	return c
}

func TestSendsToken(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("authenticationToken")
		if err != nil || cookie.Value != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	_, err := newClient(t, srv, "").Pools(context.Background())
	require.NoError(t, err)
}

func TestKeepsBasePath(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/xo/rest/v0/pools" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	_, err := newClient(t, srv, "/xo/").Pools(context.Background())
	require.NoError(t, err)
}

func TestReturnsTypedError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := newClient(t, srv, "").Pools(context.Background())

	var xoErr *xoa.Error
	require.ErrorAs(t, err, &xoErr)
	require.Equal(t, http.StatusUnauthorized, xoErr.StatusCode)
	require.Equal(t, "invalid token", xoErr.Message)
}

func TestDoesNotFollowRedirects(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/signin" {
			_, _ = w.Write([]byte(`<html></html>`))
			return
		}
		http.Redirect(w, r, "/signin", http.StatusFound)
	}))
	defer srv.Close()

	_, err := newClient(t, srv, "").Pools(context.Background())

	var xoErr *xoa.Error
	require.ErrorAs(t, err, &xoErr)
	require.Equal(t, http.StatusFound, xoErr.StatusCode)
}

func TestVerifiesTLSByDefault(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c, err := xoa.New(xoa.Config{URL: srv.URL, Token: "secret"})
	require.NoError(t, err)

	_, err = c.Pools(context.Background())
	require.Error(t, err)
	var xoErr *xoa.Error
	require.NotErrorAs(t, err, &xoErr)
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	for name, cfg := range map[string]xoa.Config{
		"empty URL":    {Token: "secret"},
		"no scheme":    {URL: "xo.example.com", Token: "secret"},
		"wrong scheme": {URL: "ftp://xo.example.com", Token: "secret"},
		"http":         {URL: "http://xo.example.com", Token: "secret"},
		"empty token":  {URL: "https://xo.example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := xoa.New(cfg)
			require.Error(t, err)
		})
	}
}

func TestCapsResponseSize(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"uuid":"` + strings.Repeat("a", 2048) + `"}]`))
	}))
	defer srv.Close()

	c, err := xoa.New(xoa.Config{URL: srv.URL, Token: "secret", InsecureSkipVerify: true, MaxResponseSize: 1024})
	require.NoError(t, err)

	_, err = c.Pools(context.Background())
	var tooLarge *http.MaxBytesError
	require.ErrorAs(t, err, &tooLarge)
}

func TestAllowsHTTPOnOptIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c, err := xoa.New(xoa.Config{URL: srv.URL, Token: "secret", AllowHTTP: true})
	require.NoError(t, err)

	_, err = c.Pools(context.Background())
	require.NoError(t, err)
}

func TestNewDoesNotLeakURLPassword(t *testing.T) {
	for name, u := range map[string]string{
		"unparsable URL": "https://user:hunter2@xo.example.com/%zz",
		"wrong scheme":   "ftp://user:hunter2@xo.example.com",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := xoa.New(xoa.Config{URL: u, Token: "secret"})
			require.Error(t, err)
			require.NotContains(t, err.Error(), "hunter2")
		})
	}
}

func TestTimesOut(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	c, err := xoa.New(xoa.Config{URL: srv.URL, Token: "secret", InsecureSkipVerify: true, Timeout: 50 * time.Millisecond})
	require.NoError(t, err)

	start := time.Now()
	_, err = c.Pools(context.Background())
	require.Error(t, err)
	require.Less(t, time.Since(start), 5*time.Second)
}
