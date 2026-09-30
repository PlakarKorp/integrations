package common

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PlakarKorp/integrations/openstack/common/keystonetest"
	"github.com/gophercloud/gophercloud/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func authURL(t *testing.T, regions ...string) string {
	t.Helper()
	url, _ := keystonetest.Serve(t, http.StatusCreated, regions...)
	return url
}

func regionsScanned(t *testing.T, authURL, region string) ([]string, error) {
	t.Helper()
	cfg, err := ParseConfig(map[string]string{
		"openstack_auth_url":                      authURL,
		"openstack_application_credential_id":     "id",
		"openstack_application_credential_secret": "secret",
		"openstack_region":                        region,
	})
	require.NoError(t, err)
	clients, err := Connect(t.Context(), cfg)
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

// The catalog offers only compute, so gophercloud's own lookup fails for Cinder.
func TestListReportsServiceMissingFromCatalog(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{
		"openstack_auth_url":                      authURL(t, "RegionOne"),
		"openstack_application_credential_id":     "id",
		"openstack_application_credential_secret": "secret",
	})
	require.NoError(t, err)
	clients, err := Connect(t.Context(), cfg)
	require.NoError(t, err)
	require.Len(t, clients, 1)

	var got error
	for _, err := range clients[0].ListVolumes(t.Context()) {
		got = err
	}
	require.ErrorIs(t, got, ErrServiceUnavailable)
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
		assert.Equal(t, denied, errors.Is(err, ErrAccessDenied), "status %d", status)
	}
}

func TestContainerURLEscapesName(t *testing.T) {
	assert.Equal(t, "https://swift/v1/AUTH_p1/my%20files%3F%231", containerURL("https://swift/v1/AUTH_p1", "my files?#1"))
}
