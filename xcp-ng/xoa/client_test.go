package xoa_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/PlakarKorp/integration-xcp-ng/xcp-ng/xoa"
	"github.com/stretchr/testify/require"
)

func newClient(t *testing.T, srv *httptest.Server, base string) *xoa.Client {
	t.Helper()
	c, err := xoa.New(xoa.Config{URL: srv.URL + base, Token: "secret", InsecureSkipVerify: true})
	require.NoError(t, err)
	return c
}

func TestGetSendsTokenAndDecodes(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("authenticationToken")
		if err != nil || cookie.Value != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/rest/v0/vms" || r.URL.Query().Get("fields") != "name_label" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`[{"name_label":"vm1"}]`))
	}))
	defer srv.Close()

	type vm struct {
		NameLabel string `json:"name_label"`
	}
	vms, err := newClient(t, srv, "").Get[[]vm](context.Background(), "vms", url.Values{"fields": {"name_label"}})
	require.NoError(t, err)
	require.Len(t, vms, 1)
	require.Equal(t, "vm1", vms[0].NameLabel)
}

func TestGetKeepsBasePath(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/xo/rest/v0/pools" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	_, err := newClient(t, srv, "/xo/").Get[[]string](context.Background(), "pools", nil)
	require.NoError(t, err)
}

func TestGetReturnsTypedError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := newClient(t, srv, "").Get[[]string](context.Background(), "vms", nil)

	var xoErr *xoa.Error
	require.ErrorAs(t, err, &xoErr)
	require.Equal(t, http.StatusUnauthorized, xoErr.StatusCode)
	require.Equal(t, "invalid token", xoErr.Message)
}

func TestGetDoesNotFollowRedirects(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/signin" {
			_, _ = w.Write([]byte(`<html></html>`))
			return
		}
		http.Redirect(w, r, "/signin", http.StatusFound)
	}))
	defer srv.Close()

	_, err := newClient(t, srv, "").Get[[]string](context.Background(), "vms", nil)

	var xoErr *xoa.Error
	require.ErrorAs(t, err, &xoErr)
	require.Equal(t, http.StatusFound, xoErr.StatusCode)
}

func TestGetVerifiesTLSByDefault(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c, err := xoa.New(xoa.Config{URL: srv.URL, Token: "secret"})
	require.NoError(t, err)

	_, err = c.Get[[]string](context.Background(), "vms", nil)
	require.Error(t, err)
	var xoErr *xoa.Error
	require.NotErrorAs(t, err, &xoErr)
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	for name, cfg := range map[string]xoa.Config{
		"empty URL":    {Token: "secret"},
		"no scheme":    {URL: "xo.example.com", Token: "secret"},
		"wrong scheme": {URL: "ftp://xo.example.com", Token: "secret"},
		"empty token":  {URL: "https://xo.example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := xoa.New(cfg)
			require.Error(t, err)
		})
	}
}
