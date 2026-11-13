package inventory

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"iter"
	"log"
	"maps"
	"net"
	"slices"
	"strings"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integrations/openstack/common"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/db/v1/instances"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
)

//go:embed schema.json
var Schema []byte

// datastoreSubClasses maps lowercase Trove datastore types to subclasses;
// any other datastore is ResourceSubClassUndefined, the map's zero value.
var datastoreSubClasses = map[string]integration.ResourceSubClass{
	"mysql":      integration.ResourceSubClassMySQL,
	"mariadb":    integration.ResourceSubClassMySQL,
	"percona":    integration.ResourceSubClassMySQL,
	"postgresql": integration.ResourceSubClassPostgreSQL,
	"mongodb":    integration.ResourceSubClassMongoDB,
	"redis":      integration.ResourceSubClassRedis,
}

// openstackAPI is what the inventory needs from an OpenStack cloud, scoped to
// one project and region.
type openstackAPI interface {
	ListServers(ctx context.Context) iter.Seq2[servers.Server, error]
	ListVolumes(ctx context.Context) iter.Seq2[volumes.Volume, error]
	// ListImages lists the Glance images owned by the client's project.
	ListImages(ctx context.Context) iter.Seq2[images.Image, error]
	ListContainers(ctx context.Context) iter.Seq2[common.Container, error]
	ListDatabases(ctx context.Context) iter.Seq2[instances.Instance, error]
	SupportsVolumes() bool

	Scope() common.Scope
}

// osInventory scans one project, through one client per region.
type osInventory struct {
	apis []openstackAPI
}

// NewInventory authenticates against Keystone and returns an inventory of the
// token's project across every region in its catalog, or only the regions
// listed in openstack_region.
func NewInventory(ctx context.Context, params map[string]string) (integration.Inventory, error) {
	cfg, err := common.ParseConfig(params)
	if err != nil {
		return nil, err
	}

	clients, err := common.Connect(ctx, cfg)
	if err != nil {
		return nil, err
	}

	inv := &osInventory{}
	for _, c := range clients {
		inv.apis = append(inv.apis, c)
	}
	return inv, nil
}

// List sends one entry per server, volume, owned Glance image, Swift container
// and Trove instance in the project, region by region. A service missing from a
// region's catalog, or refusing the credential, is skipped; any other error
// stops the listing.
func (inv *osInventory) List(ctx context.Context, resources chan<- *integration.InventoryEntry) error {
	defer close(resources)

	for entry, err := range inv.entries(ctx) {
		if err != nil {
			return err
		}
		select {
		case resources <- entry:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (inv *osInventory) Close(ctx context.Context) error {
	return nil
}

// staticEntries returns the inventory's synthetic, no-backing-resource
// entries: one "create a new volume" per region today.
func (inv *osInventory) staticEntries() []*integration.InventoryEntry {
	var out []*integration.InventoryEntry
	for _, api := range inv.apis {
		sc := api.Scope()
		// An empty region is gophercloud's "match any region" fallback for a
		// client scoped to no catalog region at all: not a real place to
		// offer "create a volume" for. Nor is a region whose catalog has no
		// Cinder at all, which Ping would fail on anyway.
		if sc.Region == "" || !api.SupportsVolumes() {
			continue
		}
		out = append(out, volumeSpawnerEntry(sc))
	}
	return out
}

// entries chains every region's listings into one lazy stream, the static
// entries first.
func (inv *osInventory) entries(ctx context.Context) iter.Seq2[*integration.InventoryEntry, error] {
	return func(yield func(*integration.InventoryEntry, error) bool) {
		for _, e := range inv.staticEntries() {
			if !yield(e, nil) {
				return
			}
		}

		for _, api := range inv.apis {
			sc := api.Scope()
			sources := []iter.Seq2[*integration.InventoryEntry, error]{
				entriesOf(sc, api.ListServers(ctx), serverEntry),
				entriesOf(sc, api.ListVolumes(ctx), volumeEntry),
				entriesOf(sc, api.ListContainers(ctx), containerEntry),
				entriesOf(sc, api.ListDatabases(ctx), databaseEntry),
			}
			for _, source := range sources {
				for entry, err := range source {
					if errors.Is(err, common.ErrServiceUnavailable) || errors.Is(err, common.ErrAccessDenied) {
						log.Printf("openstack-inventory: region %q: skipping: %v", sc.Region, err)
						break
					}
					if err != nil {
						yield(nil, fmt.Errorf("region %q: %w", sc.Region, err))
						return
					}
					if !yield(entry, nil) {
						return
					}
				}
			}
		}
	}
}

// entriesOf drops the items toEntry returns nil for.
func entriesOf[T any](sc common.Scope, seq iter.Seq2[T, error], toEntry func(common.Scope, T) *integration.InventoryEntry) iter.Seq2[*integration.InventoryEntry, error] {
	return func(yield func(*integration.InventoryEntry, error) bool) {
		for item, err := range seq {
			if err != nil {
				yield(nil, err)
				return
			}
			e := toEntry(sc, item)
			if e == nil {
				continue
			}
			if e.Tags == nil {
				e.Tags = []string{} // an entry without tags has an empty list, not none
			}
			if !yield(e, nil) {
				return
			}
		}
	}
}

// urn follows the other inventories: urn:openstack:<project>:<service>:<region>:<type>:<id>.
func urn(sc common.Scope, service, resourceType, id string) string {
	return fmt.Sprintf("urn:openstack:%s:%s:%s:%s:%s", sc.ProjectID, service, sc.Region, resourceType, id)
}

// identifierEndpoint carries the connector options a new connector for this
// resource is prefilled with.
func identifierEndpoint(sc common.Scope, id string) integration.HostEndpoint {
	return integration.HostEndpoint{
		Type:     integration.EndpointIdentifier,
		Endpoint: id,
		Attributes: map[string]string{
			"openstack_auth_url": sc.AuthURL,
			"openstack_region":   sc.Region,
		},
	}
}

func serverEntry(sc common.Scope, s servers.Server) *integration.InventoryEntry {
	e := &integration.InventoryEntry{
		Class:     integration.ResourceClassCompute,
		SubClass:  integration.ResourceSubClassUndefined,
		URN:       urn(sc, "nova", "server", s.ID),
		Name:      s.Name,
		Region:    sc.Region,
		Service:   "nova",
		Resource:  "nova:server",
		Tags:      append(metadataTags(s.Metadata), serverTags(s)...),
		Endpoints: []integration.HostEndpoint{identifierEndpoint(sc, s.ID)},
	}
	for _, addr := range serverAddresses(s) {
		e.Endpoints = append(e.Endpoints, addressEndpoint(addr))
	}
	return e
}

// serverAddresses flattens the untyped addresses, sorted by network name.
func serverAddresses(s servers.Server) []string {
	var out []string
	for _, net := range slices.Sorted(maps.Keys(s.Addresses)) {
		entries, _ := s.Addresses[net].([]any)
		for _, e := range entries {
			if m, ok := e.(map[string]any); ok {
				if addr, ok := m["addr"].(string); ok {
					out = append(out, addr)
				}
			}
		}
	}
	return out
}

func volumeEntry(sc common.Scope, v volumes.Volume) *integration.InventoryEntry {
	e := &integration.InventoryEntry{
		Class:     integration.ResourceClassBlockStorage,
		SubClass:  integration.ResourceSubClassUndefined,
		URN:       urn(sc, "cinder", "volume", v.ID),
		Name:      v.Name,
		Region:    sc.Region,
		Service:   "cinder",
		Resource:  "cinder:volume",
		Tags:      metadataTags(v.Metadata),
		Endpoints: []integration.HostEndpoint{identifierEndpoint(sc, v.ID)},
	}
	if e.Name == "" {
		e.Name = v.ID // Cinder volumes are often unnamed
	}
	return e
}

// imageEntry leaves out images without data: a snapshot of a boot-from-volume
// server is an empty Glance image whose disks live in Cinder snapshots.
// volumeSpawnerEntry is a synthetic entry with no backing Cinder resource:
// the "create a new volume" choice in a block destination's picker. Class is
// BlockStorage, not the Compute wildcard, so it only appears there — but the
// block importer shares that same class, so this also reaches the backup
// *source* picker; block/importer rejects the spawn location itself, since
// class alone can't tell source from destination apart.
func volumeSpawnerEntry(sc common.Scope) *integration.InventoryEntry {
	return &integration.InventoryEntry{
		Class:    integration.ResourceClassBlockStorage,
		SubClass: integration.ResourceSubClassUndefined,
		URN:      urn(sc, "cinder", "spawner", common.SpawnLocation),
		Name:     "Create volume in " + sc.Region,
		Region:   sc.Region,
		Service:  "cinder",
		Resource: "cinder:spawner",
		Tags:     []string{},
		Endpoints: []integration.HostEndpoint{
			identifierEndpoint(sc, common.SpawnLocation),
		},
	}
}

func containerEntry(sc common.Scope, c common.Container) *integration.InventoryEntry {
	return &integration.InventoryEntry{
		Class:     integration.ResourceClassObjectStorage,
		SubClass:  integration.ResourceSubClassUndefined,
		URN:       urn(sc, "swift", "container", c.Name),
		Name:      c.Name,
		Region:    sc.Region,
		Service:   "swift",
		Resource:  "swift:container",
		Endpoints: []integration.HostEndpoint{{Type: integration.EndpointHost, Endpoint: c.URL}},
	}
}

// The trove API is yet to be tested against a real cloud, so the entry is best-effort.
func databaseEntry(sc common.Scope, d instances.Instance) *integration.InventoryEntry {
	e := &integration.InventoryEntry{
		Class:    integration.ResourceClassDatabase,
		SubClass: datastoreSubClass(d.Datastore.Type),
		URN:      urn(sc, "trove", "instance", d.ID),
		Name:     d.Name,
		Region:   sc.Region,
		Service:  "trove",
		Resource: "trove:instance",
	}
	if d.Hostname != "" {
		e.Endpoints = append(e.Endpoints, integration.HostEndpoint{Type: integration.EndpointHost, Endpoint: d.Hostname})
	}
	addrs := d.IP // older Trove releases only report the deprecated ip field
	if len(d.Addresses) > 0 {
		addrs = nil
		for _, a := range d.Addresses {
			addrs = append(addrs, a.Address)
		}
	}
	for _, addr := range addrs {
		e.Endpoints = append(e.Endpoints, addressEndpoint(addr))
	}
	return e
}

func datastoreSubClass(datastore string) integration.ResourceSubClass {
	return datastoreSubClasses[strings.ToLower(datastore)]
}

func addressEndpoint(addr string) integration.HostEndpoint {
	t := integration.EndpointHost
	if ip := net.ParseIP(addr); ip != nil {
		t = integration.EndpointInet6
		if ip.To4() != nil {
			t = integration.EndpointInet4
		}
	}
	return integration.HostEndpoint{Type: t, Endpoint: addr}
}

// metadataTags turns user metadata into key=value tags, sorted by key.
func metadataTags(metadata map[string]string) []string {
	var out []string
	for _, key := range slices.Sorted(maps.Keys(metadata)) {
		out = append(out, key+"="+metadata[key])
	}
	return out
}

// glanceTags writes key:value image tags as key=value, the form metadata tags
// take; other tags are kept as they are.
func glanceTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		if key, value, ok := strings.Cut(tag, ":"); ok && key != "" {
			tag = key + "=" + value
		}
		out = append(out, tag)
	}
	return out
}

// serverTags needs microversion 2.26 or later.
func serverTags(s servers.Server) []string {
	if s.Tags == nil {
		return nil
	}
	return *s.Tags
}
