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

	"github.com/PlakarKorp/go-inventory-sdk/inventory"
	"github.com/PlakarKorp/integrations/openstack/common"
	"github.com/PlakarKorp/pkg"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/db/v1/instances"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
)

//go:embed schema.json
var Schema []byte

// datastoreSubClasses maps lowercase Trove datastore types to subclasses;
// any other datastore is ResourceSubClassUndefined, the map's zero value.
var datastoreSubClasses = map[string]pkg.ResourceSubClass{
	"mysql":      pkg.ResourceSubClassMySQL,
	"mariadb":    pkg.ResourceSubClassMySQL,
	"percona":    pkg.ResourceSubClassMySQL,
	"postgresql": pkg.ResourceSubClassPostgreSQL,
	"mongodb":    pkg.ResourceSubClassMongoDB,
	"redis":      pkg.ResourceSubClassRedis,
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

	Scope() common.Scope
}

// osInventory scans one project, through one client per region.
type osInventory struct {
	apis []openstackAPI
}

// NewInventory authenticates against Keystone and returns an inventory of the
// token's project across every region in its catalog, or only the regions
// listed in openstack_region.
func NewInventory(ctx context.Context, params map[string]string) (inventory.Inventory, error) {
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
func (inv *osInventory) List(ctx context.Context, resources chan<- *inventory.InventoryEntry) error {
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
func (inv *osInventory) staticEntries() []*inventory.InventoryEntry {
	var out []*inventory.InventoryEntry
	for _, api := range inv.apis {
		sc := api.Scope()
		// An empty region is gophercloud's "match any region" fallback for a
		// client scoped to no catalog region at all: not a real place to
		// offer "create a volume" for.
		if sc.Region == "" {
			continue
		}
		out = append(out, volumeSpawnerEntry(sc))
	}
	return out
}

// entries chains every region's listings into one lazy stream, the static
// entries first.
func (inv *osInventory) entries(ctx context.Context) iter.Seq2[*inventory.InventoryEntry, error] {
	return func(yield func(*inventory.InventoryEntry, error) bool) {
		for _, e := range inv.staticEntries() {
			if !yield(e, nil) {
				return
			}
		}

		for _, api := range inv.apis {
			sc := api.Scope()
			sources := []iter.Seq2[*inventory.InventoryEntry, error]{
				entriesOf(sc, api.ListServers(ctx), serverEntry),
				entriesOf(sc, api.ListVolumes(ctx), volumeEntry),
				entriesOf(sc, api.ListImages(ctx), imageEntry),
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
func entriesOf[T any](sc common.Scope, seq iter.Seq2[T, error], toEntry func(common.Scope, T) *inventory.InventoryEntry) iter.Seq2[*inventory.InventoryEntry, error] {
	return func(yield func(*inventory.InventoryEntry, error) bool) {
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

func serverEntry(sc common.Scope, s servers.Server) *inventory.InventoryEntry {
	e := &inventory.InventoryEntry{
		Class:     pkg.ResourceClassCompute,
		SubClass:  pkg.ResourceSubClassUndefined,
		URN:       urn(sc, "nova", "server", s.ID),
		Name:      s.Name,
		Region:    sc.Region,
		Service:   "nova",
		Resource:  "nova:server",
		Tags:      append(metadataTags(s.Metadata), serverTags(s)...),
		Endpoints: []inventory.HostEndpoint{{Type: inventory.EndpointIdentifier, Endpoint: s.ID}},
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

func volumeEntry(sc common.Scope, v volumes.Volume) *inventory.InventoryEntry {
	e := &inventory.InventoryEntry{
		Class:     pkg.ResourceClassBlockStorage,
		SubClass:  pkg.ResourceSubClassUndefined,
		URN:       urn(sc, "cinder", "volume", v.ID),
		Name:      v.Name,
		Region:    sc.Region,
		Service:   "cinder",
		Resource:  "cinder:volume",
		Tags:      metadataTags(v.Metadata),
		Endpoints: []inventory.HostEndpoint{{Type: inventory.EndpointIdentifier, Endpoint: v.ID}},
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
// BlockStorage, not the Compute wildcard, so it only appears there.
func volumeSpawnerEntry(sc common.Scope) *inventory.InventoryEntry {
	return &inventory.InventoryEntry{
		Class:    pkg.ResourceClassBlockStorage,
		SubClass: pkg.ResourceSubClassUndefined,
		URN:      urn(sc, "cinder", "spawner", common.SpawnLocation),
		Name:     fmt.Sprintf("Create volume in %s", sc.Region),
		Region:   sc.Region,
		Service:  "cinder",
		Resource: "cinder:spawner",
		Tags:     []string{},
		Endpoints: []inventory.HostEndpoint{
			{Type: inventory.EndpointIdentifier, Endpoint: common.SpawnLocation, Attributes: map[string]string{"openstack_region": sc.Region}},
		},
	}
}

func imageEntry(sc common.Scope, i images.Image) *inventory.InventoryEntry {
	if i.SizeBytes == 0 {
		return nil
	}
	e := &inventory.InventoryEntry{
		// pkg has no image subclass; Resource tells images and servers apart.
		Class:     pkg.ResourceClassCompute,
		SubClass:  pkg.ResourceSubClassUndefined,
		URN:       urn(sc, "glance", "image", i.ID),
		Name:      i.Name,
		Region:    sc.Region,
		Service:   "glance",
		Resource:  "glance:image",
		Tags:      glanceTags(i.Tags),
		Endpoints: []inventory.HostEndpoint{{Type: inventory.EndpointIdentifier, Endpoint: i.ID}},
	}
	if e.Name == "" {
		e.Name = i.ID
	}
	return e
}

func containerEntry(sc common.Scope, c common.Container) *inventory.InventoryEntry {
	return &inventory.InventoryEntry{
		Class:     pkg.ResourceClassObjectStorage,
		SubClass:  pkg.ResourceSubClassUndefined,
		URN:       urn(sc, "swift", "container", c.Name),
		Name:      c.Name,
		Region:    sc.Region,
		Service:   "swift",
		Resource:  "swift:container",
		Endpoints: []inventory.HostEndpoint{{Type: inventory.EndpointHost, Endpoint: c.URL}},
	}
}

// The trove API is yet to be tested against a real cloud, so the entry is best-effort.
func databaseEntry(sc common.Scope, d instances.Instance) *inventory.InventoryEntry {
	e := &inventory.InventoryEntry{
		Class:    pkg.ResourceClassDatabase,
		SubClass: datastoreSubClass(d.Datastore.Type),
		URN:      urn(sc, "trove", "instance", d.ID),
		Name:     d.Name,
		Region:   sc.Region,
		Service:  "trove",
		Resource: "trove:instance",
	}
	if d.Hostname != "" {
		e.Endpoints = append(e.Endpoints, inventory.HostEndpoint{Type: inventory.EndpointHost, Endpoint: d.Hostname})
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

func datastoreSubClass(datastore string) pkg.ResourceSubClass {
	return datastoreSubClasses[strings.ToLower(datastore)]
}

func addressEndpoint(addr string) inventory.HostEndpoint {
	t := inventory.EndpointHost
	if ip := net.ParseIP(addr); ip != nil {
		t = inventory.EndpointInet6
		if ip.To4() != nil {
			t = inventory.EndpointInet4
		}
	}
	return inventory.HostEndpoint{Type: t, Endpoint: addr}
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
