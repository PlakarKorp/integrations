package inventory

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"testing"

	"github.com/PlakarKorp/go-inventory-sdk/inventory"
	"github.com/PlakarKorp/integrations/openstack/common"
	"github.com/PlakarKorp/integrations/openstack/common/keystonetest"
	"github.com/PlakarKorp/pkg"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/db/v1/datastores"
	"github.com/gophercloud/gophercloud/v2/openstack/db/v1/instances"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/gophercloud/gophercloud/v2/openstack/objectstorage/v1/containers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubAPI serves fixed items; a non-nil err for a resource is returned
// instead of its items.
type stubAPI struct {
	region     string // defaults to RegionOne
	servers    []servers.Server
	volumes    []volumes.Volume
	images     []images.Image
	containers []common.Container
	databases  []instances.Instance

	serversErr, volumesErr, imagesErr, containersErr, databasesErr error
}

func seq[T any](items []T, err error) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		if err != nil {
			var zero T
			yield(zero, err)
			return
		}
		for _, it := range items {
			if !yield(it, nil) {
				return
			}
		}
	}
}

func (s *stubAPI) ListServers(context.Context) iter.Seq2[servers.Server, error] {
	return seq(s.servers, s.serversErr)
}
func (s *stubAPI) ListVolumes(context.Context) iter.Seq2[volumes.Volume, error] {
	return seq(s.volumes, s.volumesErr)
}
func (s *stubAPI) ListImages(context.Context) iter.Seq2[images.Image, error] {
	return seq(s.images, s.imagesErr)
}
func (s *stubAPI) ListContainers(context.Context) iter.Seq2[common.Container, error] {
	return seq(s.containers, s.containersErr)
}
func (s *stubAPI) ListDatabases(context.Context) iter.Seq2[instances.Instance, error] {
	return seq(s.databases, s.databasesErr)
}
func (s *stubAPI) Scope() common.Scope {
	return common.Scope{ProjectID: "p1", Region: cmp.Or(s.region, "RegionOne")}
}

func listAll(t *testing.T, apis ...openstackAPI) ([]*inventory.InventoryEntry, error) {
	t.Helper()
	inv := &osInventory{apis: apis}
	ch := make(chan *inventory.InventoryEntry)
	errc := make(chan error, 1)
	go func() { errc <- inv.List(t.Context(), ch) }()
	var out []*inventory.InventoryEntry
	for e := range ch {
		out = append(out, e)
	}
	return out, <-errc
}

func TestListMapsEveryResource(t *testing.T) {
	api := &stubAPI{
		servers: []servers.Server{{ID: "s1", Name: "web", Status: "ACTIVE", Addresses: map[string]any{
			"net": []any{map[string]any{"addr": "10.0.0.5"}, map[string]any{"addr": "2001:db8::5"}},
		}, Metadata: map[string]string{"team": "web", "env": "prod"}, Tags: &[]string{"frontend"}}},
		volumes: []volumes.Volume{{ID: "v1", Status: "in-use", VolumeType: "ssd", Size: 10,
			Attachments: []volumes.Attachment{{ServerID: "s1"}}, Metadata: map[string]string{"backup": "daily"}}},
		images: []images.Image{
			{ID: "i1", Name: "web-snap", Status: images.ImageStatusActive, Visibility: images.ImageVisibilityPrivate, DiskFormat: "qcow2", SizeBytes: 1024,
				Properties: map[string]any{"image_type": "snapshot"}, Tags: []string{"golden", "owner:peeyush", "env:prod"}},
			{ID: "i2", Name: "bfv-snap", Status: images.ImageStatusActive, Visibility: images.ImageVisibilityPrivate}, // no data: skipped
		},
		containers: []common.Container{{Container: containers.Container{Name: "media", Count: 3, Bytes: 42}, URL: "http://swift:8080/v1/AUTH_p1/media"}},
		databases: []instances.Instance{{ID: "d1", Name: "orders", Status: "ACTIVE", Hostname: "db.example",
			Datastore: datastores.DatastorePartial{Type: "postgresql"}, Addresses: []instances.Address{{Address: "10.0.0.9"}}}},
	}

	got, err := listAll(t, api)
	require.NoError(t, err)

	want := []*inventory.InventoryEntry{
		{
			Class: pkg.ResourceClassCompute, URN: "urn:openstack:p1:nova:RegionOne:server:s1",
			Name: "web", Region: "RegionOne", Service: "nova", Resource: "nova:server",
			Tags: []string{"env=prod", "team=web", "frontend"}, // metadata sorted by key, then Nova tags
			Endpoints: []inventory.HostEndpoint{
				{Type: inventory.EndpointIdentifier, Endpoint: "s1"},
				{Type: inventory.EndpointInet4, Endpoint: "10.0.0.5"},
				{Type: inventory.EndpointInet6, Endpoint: "2001:db8::5"},
			},
		},
		{
			Class: pkg.ResourceClassBlockStorage, URN: "urn:openstack:p1:cinder:RegionOne:volume:v1",
			Name: "v1", Region: "RegionOne", Service: "cinder", Resource: "cinder:volume",
			Tags:      []string{"backup=daily"},
			Endpoints: []inventory.HostEndpoint{{Type: inventory.EndpointIdentifier, Endpoint: "v1"}},
		},
		{
			Class: pkg.ResourceClassCompute, URN: "urn:openstack:p1:glance:RegionOne:image:i1",
			Name: "web-snap", Region: "RegionOne", Service: "glance", Resource: "glance:image",
			Tags:      []string{"golden", "owner=peeyush", "env=prod"},
			Endpoints: []inventory.HostEndpoint{{Type: inventory.EndpointIdentifier, Endpoint: "i1"}},
		},
		{
			Class: pkg.ResourceClassObjectStorage, URN: "urn:openstack:p1:swift:RegionOne:container:media",
			Name: "media", Region: "RegionOne", Service: "swift", Resource: "swift:container",
			Tags:      []string{},
			Endpoints: []inventory.HostEndpoint{{Type: inventory.EndpointHost, Endpoint: "http://swift:8080/v1/AUTH_p1/media"}},
		},
		{
			Class: pkg.ResourceClassDatabase, SubClass: pkg.ResourceSubClassPostgreSQL,
			URN:  "urn:openstack:p1:trove:RegionOne:instance:d1",
			Name: "orders", Region: "RegionOne", Service: "trove", Resource: "trove:instance",
			Tags: []string{},
			Endpoints: []inventory.HostEndpoint{
				{Type: inventory.EndpointHost, Endpoint: "db.example"},
				{Type: inventory.EndpointInet4, Endpoint: "10.0.0.9"},
			},
		},
	}
	assert.Equal(t, want, got)
}

func TestListSkipsServicesMissingFromCatalog(t *testing.T) {
	missing := fmt.Errorf("volumes: %w", common.ErrServiceUnavailable)
	api := &stubAPI{
		servers:       []servers.Server{{ID: "s1"}},
		volumesErr:    missing,
		imagesErr:     fmt.Errorf("images: %w", common.ErrServiceUnavailable),
		containersErr: fmt.Errorf("containers: %w", common.ErrServiceUnavailable),
		databasesErr:  fmt.Errorf("databases: %w", common.ErrServiceUnavailable),
	}

	got, err := listAll(t, api)
	require.NoError(t, err)
	assert.Equal(t, []string{"nova"}, serviceNames(got), "only the server should be listed")
}

func TestListSkipsServicesThatDenyAccess(t *testing.T) {
	api := &stubAPI{
		servers:       []servers.Server{{ID: "s1"}},
		containersErr: fmt.Errorf("list containers: %w", common.ErrAccessDenied),
		databasesErr:  fmt.Errorf("list databases: %w", common.ErrAccessDenied),
	}

	got, err := listAll(t, api)
	require.NoError(t, err)
	assert.Equal(t, []string{"nova"}, serviceNames(got), "the refusing services should be skipped")
}

func TestListStopsOnError(t *testing.T) {
	boom := errors.New("boom")
	api := &stubAPI{
		servers:    []servers.Server{{ID: "s1"}},
		volumesErr: boom,
		databases:  []instances.Instance{{ID: "d1"}},
	}

	got, err := listAll(t, api)
	require.ErrorIs(t, err, boom)
	assert.Equal(t, []string{"nova"}, serviceNames(got), "entries sent before the error")
}

func TestListCoversEveryRegion(t *testing.T) {
	one := &stubAPI{servers: []servers.Server{{ID: "s1"}}}
	two := &stubAPI{
		region:     "RegionTwo",
		servers:    []servers.Server{{ID: "s2"}},
		volumesErr: fmt.Errorf("volumes: %w", common.ErrServiceUnavailable),
	}

	got, err := listAll(t, one, two)
	require.NoError(t, err)
	var urns []string
	for _, e := range got {
		urns = append(urns, e.URN)
	}
	assert.Equal(t, []string{"urn:openstack:p1:nova:RegionOne:server:s1", "urn:openstack:p1:nova:RegionTwo:server:s2"}, urns)
}

func TestListNamesTheFailingRegion(t *testing.T) {
	boom := errors.New("boom")
	_, err := listAll(t, &stubAPI{}, &stubAPI{region: "RegionTwo", serversErr: boom})
	require.ErrorIs(t, err, boom)
	assert.ErrorContains(t, err, `region "RegionTwo"`)
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

func TestGlanceTags(t *testing.T) {
	assert.Equal(t,
		[]string{"owner=peeyush", "golden", "url=https://x", ":odd"},
		glanceTags([]string{"owner:peeyush", "golden", "url:https://x", ":odd"}))
	assert.Equal(t, []string{}, glanceTags(nil))
}

// Older Trove releases report addresses only in the deprecated ip field.
func TestDatabaseEntryFallsBackToIP(t *testing.T) {
	e := databaseEntry(common.Scope{}, instances.Instance{ID: "d1", IP: []string{"10.0.0.7"}})
	assert.Equal(t, []inventory.HostEndpoint{{Type: inventory.EndpointInet4, Endpoint: "10.0.0.7"}}, e.Endpoints)
}

func TestDatastoreSubClass(t *testing.T) {
	for datastore, want := range map[string]pkg.ResourceSubClass{
		"mysql": pkg.ResourceSubClassMySQL, "MariaDB": pkg.ResourceSubClassMySQL,
		"postgresql": pkg.ResourceSubClassPostgreSQL, "mongodb": pkg.ResourceSubClassMongoDB,
		"redis": pkg.ResourceSubClassRedis, "cassandra": pkg.ResourceSubClassUndefined,
	} {
		assert.Equal(t, want, datastoreSubClass(datastore), datastore)
	}
}

func serviceNames(entries []*inventory.InventoryEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Service)
	}
	return out
}

func TestNewInventoryRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]string
		want   error
	}{
		{
			name:   "no auth url",
			params: map[string]string{"openstack_username": "u", "openstack_password": "p", "openstack_project_name": "p"},
			want:   common.ErrMissingAuthURL,
		},
		{
			name:   "no credentials",
			params: map[string]string{"openstack_auth_url": "http://keystone/v3/"},
			want:   common.ErrMissingAuth,
		},
		{
			name:   "password without username",
			params: map[string]string{"openstack_auth_url": "http://keystone/v3/", "openstack_password": "p", "openstack_project_name": "p"},
			want:   common.ErrMissingAuth,
		},
		{
			name:   "password without project",
			params: map[string]string{"openstack_auth_url": "http://keystone/v3/", "openstack_username": "u", "openstack_password": "p"},
			want:   common.ErrMissingProject,
		},
		{
			name:   "application credential without secret",
			params: map[string]string{"openstack_auth_url": "http://keystone/v3/", "openstack_application_credential_id": "id"},
			want:   common.ErrPartialAppCred,
		},
		{
			name:   "application credential without id",
			params: map[string]string{"openstack_auth_url": "http://keystone/v3/", "openstack_application_credential_secret": "s"},
			want:   common.ErrPartialAppCred,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewInventory(t.Context(), tt.params)
			require.ErrorIs(t, err, tt.want)
		})
	}
}

func TestNewInventoryAuthenticates(t *testing.T) {
	tests := []struct {
		name        string
		params      map[string]string
		wantMethods []string
		wantScope   map[string]any
		check       func(t *testing.T, got *keystonetest.AuthRequest)
	}{
		{
			name: "password scoped by project name defaults the domain",
			params: map[string]string{
				"openstack_username":     "user-a",
				"openstack_password":     "secret",
				"openstack_project_name": "tenant-a",
			},
			wantMethods: []string{"password"},
			wantScope: map[string]any{"project": map[string]any{
				"name":   "tenant-a",
				"domain": map[string]any{"name": "Default"},
			}},
			check: func(t *testing.T, got *keystonetest.AuthRequest) {
				p := got.Auth.Identity.Password
				require.NotNil(t, p)
				assert.Equal(t, "user-a", p.User.Name)
				assert.Equal(t, "secret", p.User.Password)
				assert.Equal(t, "Default", p.User.Domain.Name)
			},
		},
		{
			name: "password scoped by project id wins over name",
			params: map[string]string{
				"openstack_username":     "user-a",
				"openstack_password":     "secret",
				"openstack_domain_name":  "corp",
				"openstack_project_id":   "abc123",
				"openstack_project_name": "ignored",
			},
			wantMethods: []string{"password"},
			wantScope:   map[string]any{"project": map[string]any{"id": "abc123"}},
			check: func(t *testing.T, got *keystonetest.AuthRequest) {
				p := got.Auth.Identity.Password
				require.NotNil(t, p)
				assert.Equal(t, "corp", p.User.Domain.Name)
			},
		},
		{
			name: "application credential sends no scope",
			params: map[string]string{
				"openstack_application_credential_id":     "id",
				"openstack_application_credential_secret": "s",
				"openstack_username":                      "ignored",
			},
			wantMethods: []string{"application_credential"},
			check: func(t *testing.T, got *keystonetest.AuthRequest) {
				ac := got.Auth.Identity.ApplicationCredential
				require.NotNil(t, ac)
				assert.Equal(t, "id", ac.ID)
				assert.Equal(t, "s", ac.Secret)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, got := keystonetest.Serve(t, http.StatusCreated)
			tt.params["openstack_auth_url"] = url

			inv, err := NewInventory(t.Context(), tt.params)
			require.NoError(t, err)
			t.Cleanup(func() { _ = inv.Close(context.Background()) })

			assert.Equal(t, tt.wantMethods, got.Auth.Identity.Methods)
			assert.Equal(t, tt.wantScope, got.Auth.Scope)
			tt.check(t, got)
		})
	}
}

func TestNewInventoryReportsRejectedCredentials(t *testing.T) {
	url, _ := keystonetest.Serve(t, http.StatusUnauthorized)
	_, err := NewInventory(t.Context(), map[string]string{
		"openstack_auth_url":     url,
		"openstack_username":     "user-a",
		"openstack_password":     "wrong",
		"openstack_project_name": "tenant-a",
	})
	assert.Error(t, err, "NewInventory() should fail with rejected credentials")
}

func TestListClosesChannel(t *testing.T) {
	url, _ := keystonetest.Serve(t, http.StatusCreated) // no regions: an empty catalog
	inv, err := NewInventory(t.Context(), map[string]string{
		"openstack_auth_url":                      url,
		"openstack_application_credential_id":     "id",
		"openstack_application_credential_secret": "s",
	})
	require.NoError(t, err)

	entries := make(chan *inventory.InventoryEntry)
	errc := make(chan error, 1)
	go func() { errc <- inv.List(t.Context(), entries) }()

	var listed []*inventory.InventoryEntry
	for e := range entries {
		listed = append(listed, e)
	}
	require.NoError(t, <-errc)
	assert.Empty(t, listed)
}
