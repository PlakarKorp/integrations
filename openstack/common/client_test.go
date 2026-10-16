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

// Mirrors TestListReportsServiceMissingFromCatalog: the fake catalog offers
// only compute, so staged mode must be rejected before any upload is tried.
func TestCheckUploadModeRejectsStagedWithoutObjectStorage(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{
		"openstack_auth_url":                      authURL(t, "RegionOne"),
		"openstack_application_credential_id":     "id",
		"openstack_application_credential_secret": "secret",
		"openstack_upload_mode":                   "staged",
	})
	require.NoError(t, err)
	clients, err := Connect(t.Context(), cfg)
	require.NoError(t, err)
	require.Len(t, clients, 1)

	err = CheckUploadMode(clients[0])
	require.Error(t, err)
	assert.ErrorContains(t, err, `"RegionOne"`)
	assert.ErrorContains(t, err, `"staged"`)
}

func TestCheckUploadModeAcceptsStagedWithObjectStorage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Subject-Token", "fake-token")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"token":{"expires_at":"2999-01-01T00:00:00.000000Z","project":{"id":"p1"},
			"catalog":[{"type":"object-store","endpoints":[{"interface":"public","region":"RegionOne","url":"http://swift.example/v1/AUTH_p1"}]}]}}`)
	}))
	t.Cleanup(srv.Close)

	cfg, err := ParseConfig(map[string]string{
		"openstack_auth_url":                      srv.URL + "/v3/",
		"openstack_application_credential_id":     "id",
		"openstack_application_credential_secret": "secret",
		"openstack_region":                        "RegionOne",
		"openstack_upload_mode":                   "staged",
	})
	require.NoError(t, err)
	clients, err := Connect(t.Context(), cfg)
	require.NoError(t, err)
	require.Len(t, clients, 1)

	assert.NoError(t, CheckUploadMode(clients[0]))
}

// swiftInOtherRegion serves a catalog where compute is in RegionOne and Swift
// only in DE, as on OVH.
func swiftInOtherRegion(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Subject-Token", "fake-token")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"token":{"expires_at":"2999-01-01T00:00:00.000000Z","project":{"id":"p1"},
			"catalog":[
				{"type":"compute","endpoints":[{"interface":"public","region":"RegionOne","url":"http://nova.example/v2.1"}]},
				{"type":"object-store","endpoints":[{"interface":"public","region":"DE","url":"http://swift.example/v1/AUTH_p1"}]}]}}`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v3/"
}

func TestCheckUploadModeUsesObjectStorageRegion(t *testing.T) {
	tests := []struct {
		name          string
		storageRegion string
		wantErr       string
	}{
		{name: "swift in its own region", storageRegion: "DE"},
		{name: "falls back to the connector region", wantErr: `region "RegionOne"`},
		{name: "region without a catalog entry", storageRegion: "UK1", wantErr: "not in the service catalog"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]string{
				"openstack_auth_url":                      swiftInOtherRegion(t),
				"openstack_application_credential_id":     "id",
				"openstack_application_credential_secret": "secret",
				"openstack_region":                        "RegionOne",
				"openstack_upload_mode":                   "staged",
			}
			if tt.storageRegion != "" {
				params["openstack_object_storage_region"] = tt.storageRegion
			}
			cfg, err := ParseConnectorConfig(params)
			require.NoError(t, err)
			clients, err := Connect(t.Context(), cfg)
			if tt.wantErr == "" {
				require.NoError(t, err)
				assert.NoError(t, CheckUploadMode(clients[0]))
				return
			}
			if err == nil {
				err = CheckUploadMode(clients[0])
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
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
