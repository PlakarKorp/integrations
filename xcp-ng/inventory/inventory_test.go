package inventory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/stretchr/testify/require"
)

// Recorded from XO.
const poolsJSON = `[
  {
    "uuid": "dad1e130-c99e-71c8-4ce3-d5691b6084e4",
    "name_label": "xcp-ng-nestor",
    "href": "/rest/v0/pools/dad1e130-c99e-71c8-4ce3-d5691b6084e4"
  }
]`

// The first two VMs are recorded from XO; the third is hand-written to
// cover a global IPv6 and an IPv4 link-local address.
const vmsJSON = `[
  {
    "uuid": "f6cf3ac0-a9fb-84e7-2d73-8ee16220cde7",
    "name_label": "XOA",
    "power_state": "Running",
    "tags": ["", "XOA", "2025.12"],
    "addresses": {
      "0/ipv4/0": "10.0.2.131",
      "0/ipv6/0": "fe80::c404:aeff:fe74:ed3"
    },
    "$pool": "dad1e130-c99e-71c8-4ce3-d5691b6084e4",
    "href": "/rest/v0/vms/f6cf3ac0-a9fb-84e7-2d73-8ee16220cde7"
  },
  {
    "uuid": "89ac9b41-f8fe-9179-9514-8d69ec1d37d2",
    "name_label": "debian-1",
    "power_state": "Running",
    "tags": [],
    "addresses": {},
    "$pool": "dad1e130-c99e-71c8-4ce3-d5691b6084e4",
    "href": "/rest/v0/vms/89ac9b41-f8fe-9179-9514-8d69ec1d37d2"
  },
  {
    "uuid": "3b9e1c2a-5d4f-4e6a-9b8c-7d6e5f4a3b2c",
    "name_label": "web-1",
    "power_state": "Running",
    "tags": ["prod"],
    "addresses": {
      "0/ipv4/0": "169.254.10.20",
      "0/ipv6/0": "2001:db8::10"
    },
    "$pool": "dad1e130-c99e-71c8-4ce3-d5691b6084e4",
    "href": "/rest/v0/vms/3b9e1c2a-5d4f-4e6a-9b8c-7d6e5f4a3b2c"
  }
]`

// fakeXO accepts only the token "secret". An empty vms body makes /vms fail.
func fakeXO(t *testing.T, vms string) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("authenticationToken")
		if err != nil || cookie.Value != "secret" {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/rest/v0/pools":
			_, _ = w.Write([]byte(poolsJSON))
		case r.URL.Path == "/rest/v0/vms" && vms != "":
			_, _ = w.Write([]byte(vms))
		default:
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func params(srv *httptest.Server) map[string]string {
	return map[string]string{
		"xo_url":             srv.URL,
		"xo_token":           "secret",
		"xo_tls_skip_verify": "true",
	}
}

func list(t *testing.T, inv integration.Inventory) ([]*integration.InventoryEntry, error) {
	t.Helper()
	resources := make(chan *integration.InventoryEntry)
	errc := make(chan error, 1)
	go func() { errc <- inv.List(context.Background(), resources) }()

	var entries []*integration.InventoryEntry
	for e := range resources {
		entries = append(entries, e)
	}
	return entries, <-errc
}

func TestList(t *testing.T) {
	inv, err := NewInventory(context.Background(), params(fakeXO(t, vmsJSON)))
	require.NoError(t, err)

	entries, err := list(t, inv)
	require.NoError(t, err)

	vm := func(uuid, name string, tags []string, endpoints ...integration.HostEndpoint) *integration.InventoryEntry {
		return &integration.InventoryEntry{
			Class:     integration.ResourceClassCompute,
			SubClass:  integration.ResourceSubClassUndefined,
			Tags:      tags,
			URN:       "urn:xcp-ng:pool:dad1e130-c99e-71c8-4ce3-d5691b6084e4:vm:" + uuid,
			Name:      name,
			Region:    "xcp-ng-nestor",
			Service:   "instance",
			Resource:  "compute:instance",
			Endpoints: append([]integration.HostEndpoint{{Type: integration.EndpointUUID, Endpoint: uuid}}, endpoints...),
		}
	}
	require.Equal(t, []*integration.InventoryEntry{
		vm("f6cf3ac0-a9fb-84e7-2d73-8ee16220cde7", "XOA", []string{"XOA", "2025.12"},
			integration.HostEndpoint{Type: integration.EndpointInet4, Endpoint: "10.0.2.131"}),
		vm("89ac9b41-f8fe-9179-9514-8d69ec1d37d2", "debian-1", []string{}),
		vm("3b9e1c2a-5d4f-4e6a-9b8c-7d6e5f4a3b2c", "web-1", []string{"prod"},
			integration.HostEndpoint{Type: integration.EndpointInet6, Endpoint: "2001:db8::10"}),
	}, entries)

	require.NoError(t, inv.Close(context.Background()))
}

func TestListReturnsXOError(t *testing.T) {
	inv, err := NewInventory(context.Background(), params(fakeXO(t, "")))
	require.NoError(t, err)

	entries, err := list(t, inv)
	require.Error(t, err)
	require.Empty(t, entries)
}

func TestNewInventoryRejectsInvalidConfig(t *testing.T) {
	srv := fakeXO(t, vmsJSON)
	for name, change := range map[string]func(map[string]string){
		"missing xo_url":             func(p map[string]string) { delete(p, "xo_url") },
		"missing xo_token":           func(p map[string]string) { delete(p, "xo_token") },
		"invalid xo_tls_skip_verify": func(p map[string]string) { p["xo_tls_skip_verify"] = "maybe" },
		"rejected token":             func(p map[string]string) { p["xo_token"] = "wrong" },
		"tls verified by default":    func(p map[string]string) { delete(p, "xo_tls_skip_verify") },
	} {
		t.Run(name, func(t *testing.T) {
			p := params(srv)
			change(p)
			_, err := NewInventory(context.Background(), p)
			require.Error(t, err)
		})
	}
}
