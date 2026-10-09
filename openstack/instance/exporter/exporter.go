/*
 * Copyright (c) 2026 Peeyush Aggarwal <peeyushaggarwal94@gmail.com>
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */

package exporter

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/PlakarKorp/integrations/openstack/common"
	"github.com/PlakarKorp/integrations/openstack/instance"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
)

//go:embed schema.json
var ExporterSchema []byte

type Exporter struct {
	client *common.Client
	// disks tracks each disk's restore image, until the server (for the
	// root disk) or a new volume (for an attached one) is created from it.
	disks map[string]*diskRestore
}

type diskRestore struct {
	metadata *common.DiskMetadata
	imageID  string
}

// NewExporter spawns a new server, so the location names nothing: it must
// be proto:// alone.
func NewExporter(ctx context.Context, opts *connectors.Options, proto string, params map[string]string) (exporter.Exporter, error) {
	if params["location"] != proto+"://" {
		return nil, fmt.Errorf("location: bad value: %q is not %s://", params["location"], proto)
	}
	cfg, err := common.ParseConnectorConfig(params)
	if err != nil {
		return nil, err
	}
	clients, err := common.Connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if len(clients) != 1 {
		return nil, fmt.Errorf("connect: expected one region, got %d", len(clients))
	}
	return &Exporter{client: clients[0], disks: map[string]*diskRestore{}}, nil
}

func (exp *Exporter) Origin() string        { return "" }
func (exp *Exporter) Type() string          { return instance.Protocol }
func (exp *Exporter) Root() string          { return "/" }
func (exp *Exporter) Flags() location.Flags { return 0 }

func (exp *Exporter) Ping(ctx context.Context) error {
	for _, err := range exp.client.ListServers(ctx) {
		if err != nil {
			return err
		}
		break
	}
	return nil
}

// Export expects the importer's layout. Kloset walks the snapshot in path
// order, so every .METADATA.json arrives before the disk next to it.
func (exp *Exporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	var serverMetadata *common.ServerMetadata

loop:
	for {
		var record *connectors.Record
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r, ok := <-records:
			if !ok {
				break loop
			}
			record = r
		}

		switch {
		case record.FileInfo.IsDir():
			results <- record.Ok()

		case record.Pathname == "/"+instance.MetadataName:
			if serverMetadata != nil {
				err := fmt.Errorf("duplicate %s", instance.MetadataName)
				results <- record.Error(err)
				return err
			}
			m, err := decodeServerMetadata(record)
			if err != nil {
				results <- record.Error(err)
				return err
			}
			serverMetadata = m
			results <- record.Ok()

		default:
			if err := exp.handleDisk(ctx, record); err != nil {
				results <- record.Error(err)
				return err
			}
			results <- record.Ok()
		}
	}

	if serverMetadata == nil {
		return errors.New("the snapshot has no server metadata")
	}

	root, rootID, err := exp.rootDisk()
	if err != nil {
		return err
	}

	server, err := exp.spawnServer(ctx, serverMetadata, root.imageID)
	if err != nil {
		return err
	}
	log.Printf("openstack-instance: spawned server %q from image %q", server.ID, root.imageID)
	exp.client.DeleteTempImage(ctx, root.imageID)
	root.imageID = ""

	for id, restore := range exp.disks {
		if id == rootID {
			continue
		}
		if err := exp.restoreVolume(ctx, server.ID, id, restore); err != nil {
			return err
		}
	}
	return nil
}

// handleDisk records a disk's metadata, or uploads its qcow2 to a temporary
// Glance image, keyed by the volume or root image ID in its path.
func (exp *Exporter) handleDisk(ctx context.Context, record *connectors.Record) error {
	origin, id, name, ok := diskPath(record.Pathname)
	if !ok {
		return fmt.Errorf("unsupported path %q", record.Pathname)
	}
	restore := exp.disks[id]
	if restore == nil {
		restore = new(diskRestore)
		exp.disks[id] = restore
	}

	switch name {
	case instance.MetadataName:
		if restore.metadata != nil {
			return fmt.Errorf("duplicate %s for %q", instance.MetadataName, id)
		}
		m, err := decodeDiskMetadata(record, origin, id)
		if err != nil {
			return err
		}
		restore.metadata = m
		return nil

	case instance.DiskName:
		if restore.imageID != "" {
			return fmt.Errorf("%s: a second disk for %q", record.Pathname, id)
		}
		image, err := exp.client.UploadTempImage(ctx, common.RestorePrefix+id, record.Reader, diskFormat(restore.metadata))
		if err != nil {
			return fmt.Errorf("upload disk %q: %w", id, err)
		}
		log.Printf("openstack-instance: uploaded restore image %q for %q", image.ID, id)
		restore.imageID = image.ID
		return nil

	default:
		return fmt.Errorf("unsupported path %q", record.Pathname)
	}
}

// rootDisk returns the snapshot's one Glance-origin disk: the server's root.
func (exp *Exporter) rootDisk() (*diskRestore, string, error) {
	var rootID string
	for id, restore := range exp.disks {
		if restore.metadata == nil || restore.metadata.Origin != common.DiskOriginGlance {
			continue
		}
		if rootID != "" {
			return nil, "", errors.New("the snapshot has more than one root disk")
		}
		rootID = id
	}
	if rootID == "" {
		return nil, "", errors.New("the snapshot has no root disk")
	}
	root := exp.disks[rootID]
	if root.imageID == "" {
		return nil, "", fmt.Errorf("root disk %q: image was not uploaded", rootID)
	}
	return root, rootID, nil
}

func (exp *Exporter) spawnServer(ctx context.Context, metadata *common.ServerMetadata, imageID string) (*servers.Server, error) {
	if metadata.Flavor == nil {
		return nil, errors.New("server metadata has no flavor")
	}
	flavor, err := exp.client.ResolveFlavor(ctx, metadata.Flavor.ID, metadata.Flavor.Name)
	if err != nil {
		return nil, err
	}

	resolved := make([]*networks.Network, 0, len(metadata.Networks))
	for _, net := range metadata.Networks {
		if net == nil {
			return nil, errors.New("server metadata has a nil network")
		}
		r, err := exp.client.ResolveNetwork(ctx, net.ID, net.Name)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, r)
	}

	name := metadata.Server.Name
	if name == "" {
		name = common.RestorePrefix + metadata.Server.ID
	}

	return exp.client.CreateServer(ctx, servers.CreateOpts{
		Name:             name,
		ImageRef:         imageID,
		FlavorRef:        flavor.ID,
		AvailabilityZone: metadata.Server.AvailabilityZone,
		Metadata:         metadata.Server.Metadata,
		SecurityGroups:   securityGroupNames(metadata.Server.SecurityGroups),
		Networks:         serverNetworks(resolved),
	})
}

func (exp *Exporter) restoreVolume(ctx context.Context, serverID, volumeID string, restore *diskRestore) error {
	if restore.metadata == nil || restore.metadata.Origin != common.DiskOriginCinder {
		return fmt.Errorf("%q: not a Cinder-origin disk", volumeID)
	}
	if restore.imageID == "" {
		return fmt.Errorf("volume %q: image was not uploaded", volumeID)
	}

	vol, err := exp.client.CreateVolumeFromImage(ctx, restore.imageID, restore.metadata.Volume)
	if err != nil {
		return fmt.Errorf("restore volume %q: %w", volumeID, err)
	}
	exp.client.DeleteTempImage(ctx, restore.imageID)
	restore.imageID = ""

	if err := exp.client.AttachVolume(ctx, serverID, vol.ID, volumeDevice(restore.metadata)); err != nil {
		exp.client.DeleteTempVolume(ctx, vol.ID)
		return err
	}
	log.Printf("openstack-instance: attached volume %q to server %q", vol.ID, serverID)
	return nil
}

// Close deletes the restore images a failed restore left behind.
func (exp *Exporter) Close(ctx context.Context) error {
	for _, restore := range exp.disks {
		if restore.imageID != "" {
			exp.client.DeleteTempImage(ctx, restore.imageID)
			restore.imageID = ""
		}
	}
	return nil
}

// diskPath splits a disk record's pathname into its origin, ID (an image ID
// for a Glance-origin disk, a volume ID for a Cinder-origin one), and
// filename, as instance/layout.go's RootDir/VolumeDir lay them out.
func diskPath(pathname string) (origin common.DiskOrigin, id, name string, ok bool) {
	rest, ok := strings.CutPrefix(pathname, "/block-storage/")
	if !ok {
		return "", "", "", false
	}
	dir, name, ok := strings.Cut(rest, "/")
	if !ok {
		return "", "", "", false
	}
	for _, o := range [...]common.DiskOrigin{common.DiskOriginGlance, common.DiskOriginCinder} {
		if rest, ok := strings.CutPrefix(dir, string(o)+"-"); ok && common.IDFormat.MatchString(rest) {
			return o, rest, name, true
		}
	}
	return "", "", "", false
}

func decodeServerMetadata(record *connectors.Record) (*common.ServerMetadata, error) {
	m := new(common.ServerMetadata)
	if err := json.NewDecoder(record.Reader).Decode(m); err != nil {
		return nil, fmt.Errorf("decode %s: %w", instance.MetadataName, err)
	}
	if m.Server == nil || m.Server.ID == "" {
		return nil, fmt.Errorf("%s: no server", instance.MetadataName)
	}
	return m, nil
}

// decodeDiskMetadata requires a Cinder-origin disk's volume ID and size: the
// volume is recreated from them. A Glance-origin disk (the root) has none.
func decodeDiskMetadata(record *connectors.Record, pathOrigin common.DiskOrigin, id string) (*common.DiskMetadata, error) {
	m := new(common.DiskMetadata)
	if err := json.NewDecoder(record.Reader).Decode(m); err != nil {
		return nil, fmt.Errorf("decode %s: %w", instance.MetadataName, err)
	}
	if m.Origin != pathOrigin {
		return nil, fmt.Errorf("%q: origin %q in metadata does not match path origin %q", id, m.Origin, pathOrigin)
	}
	switch pathOrigin {
	case common.DiskOriginCinder:
		switch {
		case m.Volume == nil:
			return nil, fmt.Errorf("%s: no volume", instance.MetadataName)
		case m.Volume.ID == "":
			return nil, fmt.Errorf("%s: volume has no ID", instance.MetadataName)
		case m.Volume.Size <= 0:
			return nil, fmt.Errorf("%s: volume %q has no size", instance.MetadataName, m.Volume.ID)
		}
	}
	return m, nil
}

func diskFormat(metadata *common.DiskMetadata) string {
	if metadata == nil || metadata.Image == nil || metadata.Image.DiskFormat == "" {
		return common.DefaultDiskFormat
	}
	return metadata.Image.DiskFormat
}

// volumeDevice is the original attachment's device path (e.g. /dev/vdb), or
// "" to let Nova pick one.
func volumeDevice(metadata *common.DiskMetadata) string {
	if metadata == nil || metadata.Volume == nil {
		return ""
	}
	for _, attachment := range metadata.Volume.Attachments {
		if attachment.Device != "" {
			return attachment.Device
		}
	}
	return ""
}

// serverNetworks returns Nova CreateOpts.Networks. An empty list is sent as
// "none" (microversion 2.37+) so Nova does not auto-select a tenant network.
func serverNetworks(nets []*networks.Network) any {
	if len(nets) == 0 {
		return "none"
	}
	refs := make([]servers.Network, 0, len(nets))
	for _, net := range nets {
		refs = append(refs, servers.Network{UUID: net.ID})
	}
	return refs
}

func securityGroupNames(groups []map[string]any) []string {
	names := make([]string, 0, len(groups))
	for _, group := range groups {
		if name, ok := group["name"].(string); ok && name != "" {
			names = append(names, name)
		}
	}
	return names
}
