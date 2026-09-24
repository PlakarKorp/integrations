package openstackinventory

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// authRequest is the subset of a Keystone v3 token request the tests check.
type authRequest struct {
	Auth struct {
		Identity struct {
			Methods  []string `json:"methods"`
			Password *struct {
				User struct {
					Name     string `json:"name"`
					Password string `json:"password"`
					Domain   struct {
						Name string `json:"name"`
					} `json:"domain"`
				} `json:"user"`
			} `json:"password"`
			ApplicationCredential *struct {
				ID     string `json:"id"`
				Secret string `json:"secret"`
			} `json:"application_credential"`
		} `json:"identity"`
		Scope map[string]any `json:"scope"`
	} `json:"auth"`
}

// keystone serves Keystone's token endpoint and returns the auth URL to
// configure, plus the last token request it received. It answers with status;
// on success the token is scoped to project p1 and its catalog offers a
// compute service in each of regions.
func keystone(t *testing.T, status int, regions ...string) (string, *authRequest) {
	t.Helper()
	var endpoints []string
	for _, region := range regions {
		endpoints = append(endpoints, fmt.Sprintf(
			`{"interface":"public","region":%q,"region_id":%q,"url":"http://compute.%s.example/v2.1"}`,
			region, region, region))
	}
	body := fmt.Sprintf(`{"token":{"expires_at":"2999-01-01T00:00:00.000000Z","project":{"id":"p1"},
		"catalog":[{"type":"compute","endpoints":[%s]}]}}`, strings.Join(endpoints, ","))

	got := new(authRequest)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, json.NewDecoder(r.Body).Decode(got), "decode token request")
		if status != http.StatusCreated {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("X-Subject-Token", "fake-token")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v3/", got
}

func authURL(t *testing.T, regions ...string) string {
	t.Helper()
	url, _ := keystone(t, http.StatusCreated, regions...)
	return url
}

func regionsScanned(t *testing.T, authURL, region string) ([]string, error) {
	t.Helper()
	cfg, err := parseConfig(map[string]string{
		"openstack_auth_url":                      authURL,
		"openstack_application_credential_id":     "id",
		"openstack_application_credential_secret": "secret",
		"openstack_region":                        region,
	})
	require.NoError(t, err)
	clients, err := newGopherClients(t.Context(), cfg)
	var regions []string
	for _, c := range clients {
		regions = append(regions, c.Scope().Region)
	}
	return regions, err
}

func TestClientsCoverEveryCatalogRegion(t *testing.T) {
	got, err := regionsScanned(t, authURL(t, "RegionTwo", "RegionOne"), "")
	require.NoError(t, err)
	assert.Equal(t, []string{"RegionOne", "RegionTwo"}, got)
}

func TestClientsKeepConfiguredRegions(t *testing.T) {
	got, err := regionsScanned(t, authURL(t, "RegionOne", "RegionTwo", "RegionThree"), " RegionThree, RegionOne ")
	require.NoError(t, err)
	assert.Equal(t, []string{"RegionThree", "RegionOne"}, got)
}

func TestClientsRejectUnknownRegion(t *testing.T) {
	_, err := regionsScanned(t, authURL(t, "RegionOne"), "Nowhere")
	require.Error(t, err)
	assert.ErrorContains(t, err, `"Nowhere"`)
	assert.ErrorContains(t, err, "available: RegionOne")
}

// servers.Server leaves addresses untyped, so check serverAddresses reads
// them as Nova sends them.
func TestServerAddresses(t *testing.T) {
	var s servers.Server
	err := json.Unmarshal([]byte(`{"id":"s1","addresses":{
		"b-net":[{"addr":"10.0.0.2","version":4}],
		"a-net":[{"addr":"192.0.2.1","version":4},{"addr":"2001:db8::1","version":6}]}}`), &s)
	require.NoError(t, err)
	// Sorted by network name.
	assert.Equal(t, []string{"192.0.2.1", "2001:db8::1", "10.0.0.2"}, serverAddresses(s))
}

// A token without a project must be rejected: Glance's owner filter would
// otherwise match every image.
func TestClientsRequireProject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Subject-Token", "fake-token")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"token":{"expires_at":"2999-01-01T00:00:00.000000Z","catalog":[]}}`)
	}))
	t.Cleanup(srv.Close)

	_, err := regionsScanned(t, srv.URL+"/v3/", "")
	assert.ErrorContains(t, err, "not scoped to a project")
}

func TestListErrorMarksRefusedCredentials(t *testing.T) {
	for status, denied := range map[int]bool{
		http.StatusUnauthorized: true, http.StatusForbidden: true, http.StatusInternalServerError: false,
	} {
		err := listError("containers", gophercloud.ErrUnexpectedResponseCode{Actual: status})
		assert.Equal(t, denied, errors.Is(err, errAccessDenied), "status %d", status)
	}
}

func TestContainerURLEscapesName(t *testing.T) {
	assert.Equal(t, "https://swift/v1/AUTH_p1/my%20files%3F%231", containerURL("https://swift/v1/AUTH_p1", "my files?#1"))
}
