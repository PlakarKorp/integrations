package openstackinventory

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
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/db/v1/instances"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/gophercloud/gophercloud/v2/openstack/objectstorage/v1/containers"
	"github.com/gophercloud/gophercloud/v2/pagination"
)

// errServiceUnavailable means the service is not in the catalog for the
// client's region. Deployments routinely omit services (DevStack ships
// without Swift), so callers should skip rather than fail.
var errServiceUnavailable = errors.New("service not in catalog")

// errAccessDenied means the service refused the credential (401 or 403), as
// happens when a role doesn't grant it, e.g. Swift without an object-store
// role. Callers skip the service rather than lose the whole inventory.
var errAccessDenied = errors.New("access denied")

// openstackAPI is what the inventory needs from an OpenStack cloud, scoped to
// one project and region. Listings yield gophercloud's own types.
type openstackAPI interface {
	ListServers(ctx context.Context) iter.Seq2[servers.Server, error]
	ListVolumes(ctx context.Context) iter.Seq2[volumes.Volume, error]
	// ListImages lists the Glance images owned by the client's project.
	ListImages(ctx context.Context) iter.Seq2[images.Image, error]
	ListContainers(ctx context.Context) iter.Seq2[container, error]
	ListDatabases(ctx context.Context) iter.Seq2[instances.Instance, error]

	Scope() scope
}

type scope struct {
	ProjectID string
	Region    string
}

// container is a Swift container with its full URL, which the container
// listing lacks: it comes from the account URL in the catalog.
type container struct {
	containers.Container
	URL string
}

type gopherClient struct {
	provider *gophercloud.ProviderClient
	endpoint gophercloud.EndpointOpts
	scope    scope
}

// newGopherClients authenticates once and returns one client per region to
// scan, all sharing the token. The regions are the configured ones or, by
// default, every region with a public endpoint in the token's catalog, which
// is every region the credential can reach.
func newGopherClients(ctx context.Context, cfg *config) ([]*gopherClient, error) {
	provider, err := openstack.AuthenticatedClient(ctx, cfg.auth)
	if err != nil {
		return nil, fmt.Errorf("authenticate against %s: %w", cfg.auth.IdentityEndpoint, err)
	}

	project, available := tokenScope(provider)
	// Without a project, Glance's owner filter would match every image and
	// URNs would lack their project.
	if project == "" {
		return nil, fmt.Errorf("the token from %s is not scoped to a project", cfg.auth.IdentityEndpoint)
	}
	regions := available
	if len(cfg.regions) > 0 {
		for _, region := range cfg.regions {
			if !slices.Contains(available, region) {
				return nil, fmt.Errorf("region %q is not in the service catalog (available: %s)", region, strings.Join(available, ", "))
			}
		}
		regions = cfg.regions
	}
	// With no region, gophercloud matches catalog endpoints of any region.
	if len(regions) == 0 {
		regions = []string{""}
	}

	clients := make([]*gopherClient, 0, len(regions))
	for _, region := range regions {
		clients = append(clients, &gopherClient{
			provider: provider,
			endpoint: gophercloud.EndpointOpts{Region: region},
			scope:    scope{ProjectID: project, Region: region},
		})
	}
	return clients, nil
}

// tokenScope reads the project and the sorted regions with a public endpoint
// from the Keystone token.
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

func (c *gopherClient) Scope() scope { return c.scope }

// serviceFactory is the signature shared by gophercloud's service constructors
// (openstack.NewComputeV2, NewBlockStorageV3, ...). Each one looks up its
// service's URL in the token's catalog.
type serviceFactory func(*gophercloud.ProviderClient, gophercloud.EndpointOpts) (*gophercloud.ServiceClient, error)

// resource describes how to list one kind of OpenStack resource:
//
//	newService    which service to talk to, as its gophercloud constructor
//	microversion  the API microversion to request, if the fields we read need one
//	pager         the listing request, e.g. volumes.List with its options
//	extract       how to decode one page into gophercloud structs (T)
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
		newService: openstack.NewComputeV2,
		// Nova only returns server tags from 2.26 on.
		microversion: "2.26",
		pager:        func(sc *gophercloud.ServiceClient) pagination.Pager { return servers.List(sc, servers.ListOpts{}) },
		extract:      servers.ExtractServers,
	}
	volumeResource = resource[volumes.Volume]{
		name:       "volumes",
		newService: openstack.NewBlockStorageV3,
		pager:      func(sc *gophercloud.ServiceClient) pagination.Pager { return volumes.List(sc, volumes.ListOpts{}) },
		extract:    volumes.ExtractVolumes,
	}
	containerResource = resource[containers.Container]{
		name:       "containers",
		newService: openstack.NewObjectStorageV1,
		pager: func(sc *gophercloud.ServiceClient) pagination.Pager {
			return containers.List(sc, containers.ListOpts{})
		},
		extract: containers.ExtractInfo,
	}
	databaseResource = resource[instances.Instance]{
		name:       "databases",
		newService: openstack.NewDBV1,
		pager:      instances.List,
		extract:    instances.ExtractInstances,
	}
)

func (c *gopherClient) ListServers(ctx context.Context) iter.Seq2[servers.Server, error] {
	return list(ctx, c, serverResource)
}

func (c *gopherClient) ListVolumes(ctx context.Context) iter.Seq2[volumes.Volume, error] {
	return list(ctx, c, volumeResource)
}

func (c *gopherClient) ListImages(ctx context.Context) iter.Seq2[images.Image, error] {
	return list(ctx, c, ownedImageResource(c.scope.ProjectID))
}

// ownedImageResource lists the images owned by project; Glance filters them
// server-side, leaving out public and shared images.
func ownedImageResource(project string) resource[images.Image] {
	return resource[images.Image]{
		name:       "images",
		newService: openstack.NewImageV2,
		pager: func(sc *gophercloud.ServiceClient) pagination.Pager {
			return images.List(sc, images.ListOpts{Owner: project})
		},
		extract: images.ExtractImages,
	}
}

// ListContainers adds each container's URL: the account URL, which is the
// object store's endpoint in the catalog, followed by the container name.
func (c *gopherClient) ListContainers(ctx context.Context) iter.Seq2[container, error] {
	return func(yield func(container, error) bool) {
		sc, err := c.service(containerResource.name, containerResource.newService)
		if err != nil {
			yield(container{}, err)
			return
		}
		account := strings.TrimSuffix(sc.Endpoint, "/")
		for ct, err := range pages(ctx, sc, containerResource) {
			if err != nil {
				yield(container{}, err)
				return
			}
			if !yield(container{Container: ct, URL: containerURL(account, ct.Name)}, nil) {
				return
			}
		}
	}
}

// containerURL escapes the name: Swift allows any character but "/" in it.
func containerURL(account, name string) string {
	return account + "/" + url.PathEscape(name)
}

func (c *gopherClient) ListDatabases(ctx context.Context) iter.Seq2[instances.Instance, error] {
	return list(ctx, c, databaseResource)
}

// service builds the client for one service in c's region. A service missing
// from the catalog yields errServiceUnavailable so callers can skip it.
func (c *gopherClient) service(name string, newService serviceFactory) (*gophercloud.ServiceClient, error) {
	sc, err := newService(c.provider, c.endpoint)
	if err != nil {
		if _, ok := errors.AsType[*gophercloud.ErrEndpointNotFound](err); ok {
			err = errServiceUnavailable
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return sc, nil
}

// list builds the resource's service client in c's region and yields every
// item of its listing.
func list[T any](ctx context.Context, c *gopherClient, res resource[T]) iter.Seq2[T, error] {
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

// pages walks every page of a resource listing on sc and yields each item. The
// iterator is lazy: pages are fetched only as the caller ranges over it, and
// breaking out of the loop stops paging.
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
		return fmt.Errorf("list %s: %w: %w", name, errAccessDenied, err)
	}
	return fmt.Errorf("list %s: %w", name, err)
}
