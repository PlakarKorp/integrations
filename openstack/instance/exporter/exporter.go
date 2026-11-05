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
	"github.com/PlakarKorp/integrations/openstack/common/attach"
	"github.com/PlakarKorp/integrations/openstack/instance"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
)

//go:embed schema.json
var ExporterSchema []byte

type Exporter struct {
	client *common.Client
	// transfer is how each disk reaches its volume: written to it through
	// an attachment, through Glance, or whichever works. The root disk of a
	// server to spawn goes through Glance whatever it says: Nova boots the
	// server from an image.
	transfer common.DiskTransfer
	// disks tracks each disk's restore image or written volume, until the
	// server (for the root disk) is created from the image, or the volume
	// (for an attached one) is attached to the server.
	disks map[string]*diskRestore
	// attachServerID is the existing Nova server to attach every restored
	// disk to, or "" to spawn a new one.
	attachServerID string
}

type diskRestore struct {
	metadata *common.DiskMetadata
	// imageID is the restore image, until the server or a volume is
	// created from it.
	imageID string
	// volumeID is the restored volume, once the disk was written to it
	// through an attachment instead of going through an image, until it is
	// attached to the server.
	volumeID string
}

// NewExporter spawns a new server for a bare or proto://spawn location, or
// restores onto an existing one for proto://<server-id>.
func NewExporter(ctx context.Context, opts *connectors.Options, proto string, params map[string]string) (exporter.Exporter, error) {
	suffix, ok := strings.CutPrefix(params["location"], proto+"://")
	if !ok || (suffix != "" && suffix != common.SpawnLocation && !common.IDFormat.MatchString(suffix)) {
		return nil, fmt.Errorf("location: bad value: %q is not %s://, %s://spawn, or %s://<id>", params["location"], proto, proto, proto)
	}
	cfg, err := common.ParseConnectorConfig(params)
	if err != nil {
		return nil, err
	}
	transfer, err := common.ParseDiskTransfer(params["openstack_disk_transfer"])
	if err != nil {
		return nil, fmt.Errorf("openstack_disk_transfer: %w", err)
	}
	clients, err := common.Connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if len(clients) != 1 {
		return nil, fmt.Errorf("connect: expected one region, got %d", len(clients))
	}
	attachServerID := suffix
	if attachServerID == common.SpawnLocation {
		attachServerID = ""
	}
	return &Exporter{client: clients[0], transfer: transfer, disks: map[string]*diskRestore{}, attachServerID: attachServerID}, nil
}

func (exp *Exporter) Origin() string        { return "" }
func (exp *Exporter) Type() string          { return instance.Protocol }
func (exp *Exporter) Root() string          { return "/" }
func (exp *Exporter) Flags() location.Flags { return 0 }

func (exp *Exporter) Ping(ctx context.Context) error {
	if exp.attachServerID != "" {
		_, err := exp.client.GetServer(ctx, exp.attachServerID)
		return err
	}
	for _, err := range exp.client.ListServers(ctx) {
		if err != nil {
			return err
		}
		break
	}
	return nil
}

// Export expects the importer's layout. Kloset walks the snapshot in path
// order, so the server's /.METADATA.json arrives first, and every disk's
// .METADATA.json before the disk next to it. Each disk is restored as it
// arrives (see handleDisk), into a volume written through an attachment or a
// Glance image; once the records are in, the server is spawned from the
// root image or found, and each volume created from its image, if it still
// is one, and attached.
func (exp *Exporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	// The Nova server plakar runs on, to write restored volumes through;
	// nil to go through Glance. Found before any record is read, so a
	// transfer that asked for an attachment it cannot have fails with
	// nothing uploaded.
	var host *servers.Server
	if exp.transfer != common.TransferGlance {
		var err error
		if host, err = exp.client.PreflightHost(ctx); err != nil {
			if exp.transfer == common.TransferAttach {
				return fmt.Errorf("direct attachment: %w", err)
			}
			if !errors.Is(err, attach.ErrNotAttachable) {
				log.Printf("openstack-instance: direct attachment unavailable: %v", err)
			}
			host = nil
		}
	}

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
			if err := exp.handleDisk(ctx, record, host, serverMetadata); err != nil {
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

	if exp.attachServerID != "" {
		if _, err := exp.client.GetServer(ctx, exp.attachServerID); err != nil {
			return err
		}
		if err := exp.restoreDisk(ctx, exp.attachServerID, rootID, root, rootVolume(serverMetadata, rootID)); err != nil {
			return err
		}
		for id, restore := range exp.disks {
			if id == rootID {
				continue
			}
			if err := exp.restoreDisk(ctx, exp.attachServerID, id, restore, restore.metadata.Volume); err != nil {
				return err
			}
		}
		return nil
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
		if err := exp.restoreDisk(ctx, server.ID, id, restore, restore.metadata.Volume); err != nil {
			return err
		}
	}
	return nil
}

// handleDisk records a disk's metadata, or restores the disk, keyed by the
// volume or root image ID in its path. A disk that will end up as a volume
// (see writeSource) is restored one of two ways. Through host, when there
// is one, it is written to a new volume attached to that server, to attach
// to the restored server later. Without one, or when the attach fails and
// the transfer allows it, it is uploaded to a temporary Glance image. The
// root disk of a server to spawn always is: Nova boots the server from it.
func (exp *Exporter) handleDisk(ctx context.Context, record *connectors.Record, host *servers.Server, server *common.ServerMetadata) error {
	origin, id, name, ok := diskPath(record.Pathname)
	if !ok {
		return fmt.Errorf("unsupported path %q", record.Pathname)
	}
	restore := exp.disks[id]
	if restore == nil {
		restore = new(diskRestore)
		exp.disks[id] = restore
	}

	switch {
	case name == instance.MetadataName:
		if restore.metadata != nil {
			return fmt.Errorf("duplicate %s for %q", instance.MetadataName, id)
		}
		m, err := decodeDiskMetadata(record, origin, id)
		if err != nil {
			return err
		}
		restore.metadata = m
		return nil

	case instance.DiskName.MatchString(name):
		if restore.imageID != "" || restore.volumeID != "" {
			return fmt.Errorf("%s: a second disk for %q", record.Pathname, id)
		}
		src := exp.writeSource(restore.metadata, id, server)
		if src == nil || host == nil {
			return exp.uploadDisk(ctx, record, id, restore)
		}
		// AttachRestoreVolume fails before any of the disk's bytes are
		// read: when the disk is no block device's content, or the attach
		// fails.
		w, err := exp.client.AttachRestoreVolume(ctx, host, restore.metadata, src)
		if err != nil {
			if exp.transfer == common.TransferAttach {
				return fmt.Errorf("direct attachment of disk %q: %w", id, err)
			}
			log.Printf("openstack-instance: disk %q: %v; restoring it through Glance", id, err)
			return exp.uploadDisk(ctx, record, id, restore)
		}
		log.Printf("openstack-instance: writing disk %q to new volume %q through server %q", id, w.Volume().ID, host.ID)
		return exp.writeDisk(ctx, w, record, id, restore)

	default:
		return fmt.Errorf("unsupported path %q", record.Pathname)
	}
}

// writeSource is the volume the disk's restored volume is shaped like, when
// the disk ends up as a volume that could be written through an attachment:
// an attached volume's own, or for the root disk restored onto an existing
// server one sized from the flavor. nil when the disk must go through
// Glance: the root disk of a server to spawn, which Nova boots from an
// image, or a disk whose metadata, or whose server's, has not arrived.
func (exp *Exporter) writeSource(metadata *common.DiskMetadata, id string, server *common.ServerMetadata) *volumes.Volume {
	switch {
	case metadata == nil:
		return nil
	case metadata.Origin == common.DiskOriginCinder:
		return metadata.Volume
	case metadata.Origin == common.DiskOriginGlance && exp.attachServerID != "":
		return rootVolume(server, id)
	}
	return nil
}

// rootVolume creates a cinder volume that needs to end up as a glance image.
// Because cinder carries a Volume struct, it can easily be created back from the snapshot.
// Glance images do not carry Volume structs, so this creates mimics a cinder block with server metadata.
func rootVolume(server *common.ServerMetadata, rootID string) *volumes.Volume {
	if server == nil {
		return nil
	}
	src := &volumes.Volume{ID: rootID, AvailabilityZone: server.Server.AvailabilityZone}
	if server.Flavor != nil {
		src.Size = server.Flavor.Disk
	}
	return src
}

// writeDisk writes the disk's bytes to the attached volume, which is then
// the disk's restored one, restore.volumeID, to attach to the server later.
// There is no falling back from here: a failure is the restore's.
func (exp *Exporter) writeDisk(ctx context.Context, w *common.VolumeWrite, record *connectors.Record, id string, restore *diskRestore) error {
	vol, err := w.Copy(ctx, record.Reader)
	if err != nil {
		return fmt.Errorf("write disk %q: %w", id, err)
	}
	log.Printf("openstack-instance: restored volume %q for %q by writing it", vol.ID, id)
	restore.volumeID = vol.ID
	return nil
}

// uploadDisk uploads the disk to a Glance image, restore.imageID, inline,
// so the record's answer reports the upload's result. Only the image
// properties a restore needs, e.g. cinder_encryption_key_id, are carried
// over; the rest are not.
func (exp *Exporter) uploadDisk(ctx context.Context, record *connectors.Record, id string, restore *diskRestore) error {
	image, err := exp.client.UploadTempImage(ctx, common.RestorePrefix+id, record.Reader, common.DiskFormat(restore.metadata), common.RestoreImageProperties(restore.metadata))
	if err != nil {
		return fmt.Errorf("upload disk %q: %w", id, err)
	}
	log.Printf("openstack-instance: uploaded restore image %q for %q", image.ID, id)
	restore.imageID = image.ID
	return nil
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
	if root.imageID == "" && root.volumeID == "" {
		return nil, "", fmt.Errorf("root disk %q: the snapshot does not contain a glance image to restore", rootID)
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

// restoreDisk attaches a disk's restored volume to serverID: the volume the
// disk was written to, or else one created from its restore image, shaped
// like src (see writeSource). A volume that fails to attach is deleted.
func (exp *Exporter) restoreDisk(ctx context.Context, serverID, diskID string, restore *diskRestore, src *volumes.Volume) error {
	volumeID := restore.volumeID
	if volumeID == "" {
		if restore.imageID == "" {
			return fmt.Errorf("disk %q: the snapshot has no disk for it", diskID)
		}
		vol, err := exp.client.CreateVolumeFromImage(ctx, restore.imageID, src)
		if err != nil {
			return fmt.Errorf("restore volume %q: %w", diskID, err)
		}
		exp.client.DeleteTempImage(ctx, restore.imageID)
		restore.imageID = ""
		volumeID = vol.ID
	}

	// The volume is the server's once attached, or gone: not Close's.
	restore.volumeID = ""
	if err := exp.client.AttachVolume(ctx, serverID, volumeID, volumeDevice(restore.metadata)); err != nil {
		exp.client.DeleteTempVolume(ctx, volumeID)
		return err
	}
	log.Printf("openstack-instance: attached volume %q to server %q", volumeID, serverID)
	return nil
}

// Close deletes what a failed restore left behind: the restore images, and
// the volumes written but not yet attached to the server.
func (exp *Exporter) Close(ctx context.Context) error {
	for _, restore := range exp.disks {
		if restore.imageID != "" {
			exp.client.DeleteTempImage(ctx, restore.imageID)
			restore.imageID = ""
		}
		if restore.volumeID != "" {
			exp.client.DeleteTempVolume(ctx, restore.volumeID)
			restore.volumeID = ""
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
	case common.DiskOriginGlance:
	}
	return m, nil
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
