package xoa_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
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

func TestVMs(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v0/vms" || r.URL.Query().Get("fields") != "uuid,name_label,power_state,tags,addresses,$pool" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Recorded from XO.
		_, _ = w.Write([]byte(`[
  {
    "uuid": "f6cf3ac0-a9fb-84e7-2d73-8ee16220cde7",
    "name_label": "XOA",
    "power_state": "Running",
    "tags": [
      "",
      "XOA",
      "2025.12"
    ],
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
  }
]`))
	}))
	defer srv.Close()

	vms, err := newClient(t, srv, "").VMs(context.Background())
	require.NoError(t, err)
	require.Equal(t, []xoa.VM{
		{
			UUID:       "f6cf3ac0-a9fb-84e7-2d73-8ee16220cde7",
			NameLabel:  "XOA",
			PowerState: "Running",
			Tags:       []string{"", "XOA", "2025.12"},
			Addresses: map[string]string{
				"0/ipv4/0": "10.0.2.131",
				"0/ipv6/0": "fe80::c404:aeff:fe74:ed3",
			},
			PoolUUID: "dad1e130-c99e-71c8-4ce3-d5691b6084e4",
		},
		{
			UUID:       "89ac9b41-f8fe-9179-9514-8d69ec1d37d2",
			NameLabel:  "debian-1",
			PowerState: "Running",
			Tags:       []string{},
			Addresses:  map[string]string{},
			PoolUUID:   "dad1e130-c99e-71c8-4ce3-d5691b6084e4",
		},
	}, vms)
}

func TestVMIPs(t *testing.T) {
	for name, tc := range map[string]struct {
		addresses map[string]string
		want      []netip.Addr
	}{
		"no guest tools": {
			addresses: map[string]string{},
		},
		"ipv4 and ipv6": {
			addresses: map[string]string{
				"0/ipv6/0": "fe80::c404:aeff:fe74:ed3",
				"0/ipv4/0": "10.0.2.131",
			},
			want: []netip.Addr{
				netip.MustParseAddr("10.0.2.131"),
				netip.MustParseAddr("fe80::c404:aeff:fe74:ed3"),
			},
		},
		"several nics": {
			addresses: map[string]string{
				"1/ipv4/0": "192.168.1.10",
				"0/ipv4/1": "10.0.2.132",
				"0/ipv4/0": "10.0.2.131",
			},
			want: []netip.Addr{
				netip.MustParseAddr("10.0.2.131"),
				netip.MustParseAddr("10.0.2.132"),
				netip.MustParseAddr("192.168.1.10"),
			},
		},
		"legacy duplicate key": {
			addresses: map[string]string{
				"0/ip":     "10.0.2.131",
				"0/ipv4/0": "10.0.2.131",
			},
			want: []netip.Addr{netip.MustParseAddr("10.0.2.131")},
		},
		"unparsable value": {
			addresses: map[string]string{
				"0/ipv4/0": "",
				"0/ipv4/1": "not-an-ip",
				"0/ipv6/0": "2001:db8::1",
			},
			want: []netip.Addr{netip.MustParseAddr("2001:db8::1")},
		},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, xoa.VM{Addresses: tc.addresses}.IPs())
		})
	}
}
