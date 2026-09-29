package common

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	gcopenstack "github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/db/v1/instances"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/gophercloud/gophercloud/v2/openstack/objectstorage/v1/containers"
	"github.com/gophercloud/gophercloud/v2/pagination"
)

// ErrServiceUnavailable means the region's catalog lacks the service, as
// DevStack lacks Swift. Callers skip it.
var ErrServiceUnavailable = errors.New("service not in catalog")

// ErrAccessDenied means the service refused the credential (401 or 403), e.g.
// Swift without an object-store role. Callers skip it.
var ErrAccessDenied = errors.New("access denied")

type Scope struct {
	ProjectID string
	Region    string
}

// Container adds the URL the Swift listing lacks.
type Container struct {
	containers.Container
	URL string
}

type Client struct {
	provider *gophercloud.ProviderClient
	endpoint gophercloud.EndpointOpts
	scope    Scope
}

// Connect authenticates once and returns one client per region: the
// configured ones, or every region with a public endpoint in the catalog.
func Connect(ctx context.Context, cfg *Config) ([]*Client, error) {
	provider, err := gcopenstack.AuthenticatedClient(ctx, cfg.auth)
	if err != nil {
		return nil, fmt.Errorf("authenticate against %q: %w", cfg.auth.IdentityEndpoint, err)
	}

	project, available := tokenScope(provider)
	// Without a project, Glance's owner filter would match every image and
	// URNs would lack their project.
	if project == "" {
		return nil, fmt.Errorf("the token from %q is not scoped to a project", cfg.auth.IdentityEndpoint)
	}
	for _, region := range cfg.regions {
		if !slices.Contains(available, region) {
			return nil, fmt.Errorf("region %q is not in the service catalog (available: %s)", region, strings.Join(available, ", "))
		}
	}
	regions := cfg.regions
	if len(regions) == 0 {
		regions = available
	}
	// With no region, gophercloud matches catalog endpoints of any region.
	if len(regions) == 0 {
		regions = []string{""}
	}

	clients := make([]*Client, 0, len(regions))
	for _, region := range regions {
		clients = append(clients, &Client{
			provider: provider,
			endpoint: gophercloud.EndpointOpts{Region: region},
			scope:    Scope{ProjectID: project, Region: region},
		})
	}
	return clients, nil
}

// tokenScope returns the token's project and its sorted public regions.
func tokenScope(provider *gophercloud.ProviderClient) (string, []string) {
	token, ok := provider.GetAuthResult().(tokens.CreateResult)
	if !ok {
		return "", nil
	}
	var project string
	if p, err := token.ExtractProject(); err == nil && p != nil {
		project = p.ID
	}
	var regions []string
	if catalog, err := token.ExtractServiceCatalog(); err == nil {
		for _, entry := range catalog.Entries {
			for _, ep := range entry.Endpoints {
				region := cmp.Or(ep.Region, ep.RegionID)
				if ep.Interface == string(gophercloud.AvailabilityPublic) && region != "" && !slices.Contains(regions, region) {
					regions = append(regions, region)
				}
			}
		}
	}
	slices.Sort(regions)
	return project, regions
}

func (c *Client) Scope() Scope { return c.scope }

// serviceFactory is the signature of gcopenstack.NewComputeV2 and its siblings.
type serviceFactory func(*gophercloud.ProviderClient, gophercloud.EndpointOpts) (*gophercloud.ServiceClient, error)

// resource describes how to list one kind of OpenStack resource.
type resource[T any] struct {
	name         string
	newService   serviceFactory
	microversion string
	pager        func(*gophercloud.ServiceClient) pagination.Pager
	extract      func(pagination.Page) ([]T, error)
}

var (
	serverResource = resource[servers.Server]{
		name:       "servers",
		newService: gcopenstack.NewComputeV2,
		// Nova only returns server tags from 2.26 on.
		microversion: "2.26",
		pager:        func(sc *gophercloud.ServiceClient) pagination.Pager { return servers.List(sc, servers.ListOpts{}) },
		extract:      servers.ExtractServers,
	}
	volumeResource = resource[volumes.Volume]{
		name:       "volumes",
		newService: gcopenstack.NewBlockStorageV3,
		pager:      func(sc *gophercloud.ServiceClient) pagination.Pager { return volumes.List(sc, volumes.ListOpts{}) },
		extract:    volumes.ExtractVolumes,
	}
	containerResource = resource[containers.Container]{
		name:       "containers",
		newService: gcopenstack.NewObjectStorageV1,
		pager: func(sc *gophercloud.ServiceClient) pagination.Pager {
			return containers.List(sc, containers.ListOpts{})
		},
		extract: containers.ExtractInfo,
	}
	databaseResource = resource[instances.Instance]{
		name:       "databases",
		newService: gcopenstack.NewDBV1,
		pager:      instances.List,
		extract:    instances.ExtractInstances,
	}
)

func (c *Client) ListServers(ctx context.Context) iter.Seq2[servers.Server, error] {
	return list(ctx, c, serverResource)
}

func (c *Client) ListVolumes(ctx context.Context) iter.Seq2[volumes.Volume, error] {
	return list(ctx, c, volumeResource)
}

func (c *Client) ListImages(ctx context.Context) iter.Seq2[images.Image, error] {
	return list(ctx, c, ownedImageResource(c.scope.ProjectID))
}

// ownedImageResource leaves out public and shared images.
func ownedImageResource(project string) resource[images.Image] {
	return resource[images.Image]{
		name:       "images",
		newService: gcopenstack.NewImageV2,
		pager: func(sc *gophercloud.ServiceClient) pagination.Pager {
			return images.List(sc, images.ListOpts{Owner: project})
		},
		extract: images.ExtractImages,
	}
}

// ListContainers builds each container's URL from the catalog's account URL.
func (c *Client) ListContainers(ctx context.Context) iter.Seq2[Container, error] {
	return func(yield func(Container, error) bool) {
		sc, err := c.service(containerResource.name, containerResource.newService)
		if err != nil {
			yield(Container{}, err)
			return
		}
		account := strings.TrimSuffix(sc.Endpoint, "/")
		for ct, err := range pages(ctx, sc, containerResource) {
			if err != nil {
				yield(Container{}, err)
				return
			}
			if !yield(Container{Container: ct, URL: containerURL(account, ct.Name)}, nil) {
				return
			}
		}
	}
}

// containerURL escapes the name: Swift allows any character but "/" in it.
func containerURL(account, name string) string {
	return account + "/" + url.PathEscape(name)
}

func (c *Client) ListDatabases(ctx context.Context) iter.Seq2[instances.Instance, error] {
	return list(ctx, c, databaseResource)
}

// service maps a service missing from the catalog to ErrServiceUnavailable.
func (c *Client) service(name string, newService serviceFactory) (*gophercloud.ServiceClient, error) {
	sc, err := newService(c.provider, c.endpoint)
	if err != nil {
		if _, ok := errors.AsType[*gophercloud.ErrEndpointNotFound](err); ok {
			err = ErrServiceUnavailable
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return sc, nil
}

func list[T any](ctx context.Context, c *Client, res resource[T]) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		sc, err := c.service(res.name, res.newService)
		if err != nil {
			var zero T
			yield(zero, err)
			return
		}
		pages(ctx, sc, res)(yield)
	}
}

// pages fetches lazily; breaking out of the loop stops paging.
func pages[T any](ctx context.Context, sc *gophercloud.ServiceClient, res resource[T]) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		sc.Microversion = res.microversion

		stopped := false
		err := res.pager(sc).EachPage(ctx, func(_ context.Context, page pagination.Page) (bool, error) {
			items, err := res.extract(page)
			if err != nil {
				return false, err
			}
			for _, item := range items {
				if !yield(item, nil) {
					stopped = true
					return false, nil
				}
			}
			return true, nil
		})
		if err != nil && !stopped {
			var zero T
			yield(zero, listError(res.name, err))
		}
	}
}

func listError(name string, err error) error {
	if gophercloud.ResponseCodeIs(err, http.StatusUnauthorized) || gophercloud.ResponseCodeIs(err, http.StatusForbidden) {
		return fmt.Errorf("list %s: %w: %w", name, ErrAccessDenied, err)
	}
	return fmt.Errorf("list %s: %w", name, err)
}
