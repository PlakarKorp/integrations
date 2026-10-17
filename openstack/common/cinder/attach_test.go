package cinder

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/PlakarKorp/integrations/openstack/common"
	"github.com/PlakarKorp/integrations/openstack/common/keystonetest"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const hostID = "host-1"

func connectCloud(t *testing.T, cloud *keystonetest.Cloud) *common.Client {
	t.Helper()
	cfg, err := common.ParseConnectorConfig(map[string]string{
		"openstack_auth_url":                      cloud.AuthURL,
		"openstack_application_credential_id":     "id",
		"openstack_application_credential_secret": "secret",
		"openstack_region":                        keystonetest.Region,
	})
	require.NoError(t, err)
	clients, err := common.Connect(t.Context(), cfg)
	require.NoError(t, err)
	return clients[0]
}

// fakeHost is how Nova answers for the server the test host pretends to be.
type fakeHost struct {
	Status   string // the server's status; ACTIVE at first
	NovaDown bool   // answer 500 instead
}

// onCloud makes the test host look like a Nova server of cloud, and keeps
// DMI out of the picture.
func onCloud(t *testing.T, cloud *keystonetest.Cloud) *fakeHost {
	t.Helper()
	host := &fakeHost{Status: "ACTIVE"}
	cloud.HostID = hostID
	cloud.Overrule("GET /compute/servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case host.NovaDown:
			w.WriteHeader(http.StatusInternalServerError)
		case r.PathValue("id") != hostID:
			w.WriteHeader(http.StatusNotFound)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"server":{"id":"` + hostID + `","status":"` + host.Status + `"}}`))
		}
	})
	setMetadataURL(t, cloud.MetadataURL)
	setDMIDir(t, t.TempDir())
	return host
}

func setMetadataURL(t *testing.T, url string) {
	t.Helper()
	saved := MetadataURL
	MetadataURL = url
	t.Cleanup(func() { MetadataURL = saved })
}

func setDMIDir(t *testing.T, dir string) {
	t.Helper()
	saved := dmiDir
	dmiDir = dir
	t.Cleanup(func() { dmiDir = saved })
}

// unreachableURL is a URL nothing listens on.
func unreachableURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	return srv.URL + "/meta_data.json"
}

// fakeDMI writes the SMBIOS fields a hypervisor exposes, and points dmiDir at
// them.
func fakeDMI(t *testing.T, product, uuid string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "product_name"), []byte(product+"\n"), 0o600))
	if uuid != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "product_uuid"), []byte(uuid+"\n"), 0o600))
	}
	setDMIDir(t, dir)
}

func TestPreflightFindsHost(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	onCloud(t, cloud)

	server, err := Preflight(t.Context(), connectCloud(t, cloud), &volumes.Volume{ID: "vol-1"})
	require.NoError(t, err)
	assert.Equal(t, hostID, server.ID)
}

func TestPreflightOffCloud(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	setMetadataURL(t, cloud.MetadataURL) // answers 404: not a Nova server
	fakeDMI(t, "OpenStack Nova", hostID) // must not be consulted

	_, err := Preflight(t.Context(), connectCloud(t, cloud), &volumes.Volume{ID: "vol-1"})
	require.ErrorIs(t, err, ErrNotAttachable)
	assert.ErrorContains(t, err, "not a Nova server: metadata service: 404")
}

func TestPreflightFallsBackToDMI(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	onCloud(t, cloud)
	setMetadataURL(t, unreachableURL(t))
	// Nova's libvirt driver stamps the ID upper-case in some versions.
	fakeDMI(t, "OpenStack Nova", "HOST-1")

	server, err := Preflight(t.Context(), connectCloud(t, cloud), &volumes.Volume{ID: "vol-1"})
	require.NoError(t, err)
	assert.Equal(t, hostID, server.ID)
}

func TestPreflightDMIRefusals(t *testing.T) {
	tests := []struct {
		name, product, uuid, want string
	}{
		{name: "no DMI", want: "product_name"},
		{name: "another hypervisor", product: "VMware Virtual Platform", want: `product "VMware Virtual Platform" is not OpenStack`},
		{name: "uuid unreadable", product: "OpenStack Nova", want: "product_uuid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloud := keystonetest.NewCloud(t)
			setMetadataURL(t, unreachableURL(t))
			if tt.product == "" {
				setDMIDir(t, filepath.Join(t.TempDir(), "missing"))
			} else {
				fakeDMI(t, tt.product, tt.uuid)
			}

			_, err := Preflight(t.Context(), connectCloud(t, cloud), &volumes.Volume{ID: "vol-1"})
			require.ErrorIs(t, err, ErrNotAttachable)
			assert.ErrorContains(t, err, "metadata service: unreachable")
			assert.ErrorContains(t, err, "DMI: ")
			assert.ErrorContains(t, err, tt.want)
		})
	}
}

func TestPreflightOtherProject(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	onCloud(t, cloud)
	cloud.HostProject = "p2"

	_, err := Preflight(t.Context(), connectCloud(t, cloud), &volumes.Volume{ID: "vol-1"})
	require.ErrorIs(t, err, ErrNotAttachable)
	assert.ErrorContains(t, err, `server "host-1" is in project "p2", the credential in "p1"`)
}

// The metadata service names a server Nova does not know of: another cloud
// or region.
func TestPreflightServerNotInCloud(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	onCloud(t, cloud)
	cloud.HostID = "host-2"

	_, err := Preflight(t.Context(), connectCloud(t, cloud), &volumes.Volume{ID: "vol-1"})
	require.ErrorIs(t, err, ErrNotAttachable)
	assert.ErrorContains(t, err, `server "host-2" (from the metadata service) is not in this cloud, project or region`)
}

func TestPreflightServerNotActive(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	onCloud(t, cloud).Status = "SHUTOFF"

	_, err := Preflight(t.Context(), connectCloud(t, cloud), &volumes.Volume{ID: "vol-1"})
	require.ErrorIs(t, err, ErrNotAttachable)
	assert.ErrorContains(t, err, `server "host-1" is SHUTOFF`)
}

func TestPreflightVolumeAlreadyAttached(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	onCloud(t, cloud)

	vol := &volumes.Volume{ID: "vol-1", Attachments: []volumes.Attachment{{ServerID: "other"}, {ServerID: hostID}}}
	_, err := Preflight(t.Context(), connectCloud(t, cloud), vol)
	require.ErrorIs(t, err, ErrNotAttachable)
	assert.ErrorContains(t, err, `volume "vol-1" is already attached to server "host-1"`)
}

// A volume attached elsewhere is not ruled out: what a backup attaches is a
// volume made from its snapshot.
func TestPreflightIgnoresOtherAttachments(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	onCloud(t, cloud)

	vol := &volumes.Volume{ID: "vol-1", Status: "in-use", Attachments: []volumes.Attachment{{ServerID: "other"}}}
	_, err := Preflight(t.Context(), connectCloud(t, cloud), vol)
	require.NoError(t, err)
}

// A Nova failure is not a no: Preflight could not tell.
func TestPreflightNovaFails(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	onCloud(t, cloud).NovaDown = true

	_, err := Preflight(t.Context(), connectCloud(t, cloud), &volumes.Volume{ID: "vol-1"})
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNotAttachable)
	assert.ErrorContains(t, err, `get server "host-1"`)
}
