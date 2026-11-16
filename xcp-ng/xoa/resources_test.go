package xoa_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PlakarKorp/integration-xcp-ng/xcp-ng/xoa"
	"github.com/stretchr/testify/require"
)

func TestPools(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v0/pools" || r.URL.Query().Get("fields") != "uuid,name_label" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Recorded from XO.
		_, _ = w.Write([]byte(`[
  {
    "uuid": "dad1e130-c99e-71c8-4ce3-d5691b6084e4",
    "name_label": "xcp-ng-nestor",
    "href": "/rest/v0/pools/dad1e130-c99e-71c8-4ce3-d5691b6084e4"
  }
]`))
	}))
	defer srv.Close()

	pools, err := newClient(t, srv, "").Pools(context.Background())
	require.NoError(t, err)
	require.Equal(t, []xoa.Pool{
		{UUID: "dad1e130-c99e-71c8-4ce3-d5691b6084e4", NameLabel: "xcp-ng-nestor"},
	}, pools)
}
