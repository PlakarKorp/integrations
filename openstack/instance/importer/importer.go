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

package importer

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/PlakarKorp/integrations/openstack/block"
	"github.com/PlakarKorp/integrations/openstack/common"
	"github.com/PlakarKorp/integrations/openstack/common/attach"
	"github.com/PlakarKorp/integrations/openstack/instance"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
)

//go:embed schema.json
var ImporterSchema []byte

type Importer struct {
	client   *common.Client
	serverID string
	// transfer is how each disk's bytes are read: from a block device of
	// the host, through Glance, or whichever works.
	transfer common.DiskTransfer

	// One Cleanup per disk: the root image, and each volume's snapshot,
	// temporary volume and its attachment or image. Closing a disk's reader
	// runs its own; Close runs them all.
	mu       sync.Mutex
	cleanups []*common.Cleanup
}

// NewImporter takes openstack-instance://<server-id>.
func NewImporter(ctx context.Context, opts *connectors.Options, proto string, params map[string]string) (importer.Importer, error) {
	// A server ID reaches a URL path: validate it as a volume ID is.
	serverID := strings.TrimPrefix(params["location"], proto+"://")
	if !common.IDFormat.MatchString(serverID) {
		return nil, fmt.Errorf("location: bad value: %q is not %s://<server-id>", params["location"], proto)
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
	return &Importer{client: clients[0], serverID: serverID, transfer: transfer}, nil
}

func (imp *Importer) Origin() string { return imp.serverID }
func (imp *Importer) Type() string   { return instance.Protocol }
func (imp *Importer) Root() string   { return "/" }

// FLAG_STREAM: Import is not safe to call twice, it creates a real server image.
func (imp *Importer) Flags() location.Flags { return location.FLAG_STREAM }

func (imp *Importer) Ping(ctx context.Context) error {
	_, err := imp.client.GetServer(ctx, imp.serverID)
	return err
}

// Import emits the instance layout (see instance/layout.go): the server's
// metadata, its root disk, then each attached volume, each its own Cleanup.
func (imp *Importer) Import(ctx context.Context, records chan<- *connectors.Record, _ <-chan *connectors.Result) error {
	defer close(records)

	server, err := imp.client.GetServer(ctx, imp.serverID)
	if err != nil {
		return err
	}

	// The Nova server plakar runs on, to attach each disk to; nil to read
	// them through Glance. Found before anything is created, so a transfer
	// that asked for an attachment it cannot have leaves nothing behind.
	var host *servers.Server
	if imp.transfer != common.TransferGlance {
		if host, err = imp.client.PreflightHost(ctx); err != nil {
			if imp.transfer == common.TransferAttach {
				return fmt.Errorf("direct attachment: %w", err)
			}
			if !errors.Is(err, attach.ErrNotAttachable) {
				log.Printf("openstack-instance: direct attachment unavailable: %v", err)
			}
			host = nil
		}
	}

	metadata, err := imp.client.ServerMetadata(ctx, server)
	if err != nil {
		return err
	}
	if err := imp.emit(ctx, records, "/"+instance.MetadataName, common.JSONReader(metadata)); err != nil {
		return err
	}

	if err := imp.emitDir(ctx, records, "/block-storage"); err != nil {
		return err
	}

	rootCleanup := imp.addCleanup()
	image, err := imp.client.CreateServerImage(ctx, server, rootCleanup)
	if err != nil {
		return err
	}
	root, err := imp.readRootDisk(ctx, host, metadata, image, rootCleanup)
	if err != nil {
		return err
	}
	if err := imp.emitDisk(ctx, records, instance.RootDir(image.ID), root.Metadata, root.Reader); err != nil {
		return err
	}

	for _, av := range server.AttachedVolumes {
		vol, err := imp.client.GetVolume(ctx, av.ID)
		if err != nil {
			return err
		}
		cleanup := imp.addCleanup()
		snap, err := imp.client.SnapshotVolume(ctx, vol.ID, cleanup)
		if err != nil {
			return err
		}
		tmp, err := imp.client.CreateVolumeFromSnapshot(ctx, snap, cleanup)
		if err != nil {
			return err
		}
		disk, err := imp.readVolumeDisk(ctx, host, tmp, common.CinderDiskMetadata(vol, snap), cleanup)
		if err != nil {
			return err
		}
		if err := imp.emitDisk(ctx, records, instance.VolumeDir(av.ID), disk.Metadata, disk.Reader); err != nil {
			return err
		}
	}
	return nil
}

// readRootDisk reads the server's root image one of two ways. Through host,
// when there is one, it is materialized as a temporary Cinder volume,
// attached to that server and read raw from the block device it turns up
// as. Without one, or when that fails and the transfer allows it, it is
// downloaded from Glance.
func (imp *Importer) readRootDisk(ctx context.Context, host *servers.Server, metadata *common.ServerMetadata, image *images.Image, cleanup *common.Cleanup) (*common.Disk, error) {
	diskMetadata := common.GlanceDiskMetadata(image)
	if host == nil {
		return imp.client.NewGlanceDisk(ctx, image, diskMetadata, cleanup), nil
	}
	tmp, err := imp.client.CreateTempVolumeFromImage(ctx, image.ID, common.TempVolumeOpts(metadata.Server, metadata), cleanup)
	if err != nil {
		if imp.transfer == common.TransferAttach {
			return nil, fmt.Errorf("direct attachment of image %q: %w", image.ID, err)
		}
		log.Printf("openstack-instance: image %q: %v; reading it through Glance", image.ID, err)
		return imp.client.NewGlanceDisk(ctx, image, diskMetadata, cleanup), nil
	}
	// NewCinderDisk fails before any of the volume's bytes are read, with
	// the volume detached again.
	disk, err := imp.client.NewCinderDisk(ctx, host, tmp, diskMetadata, cleanup)
	if err != nil {
		if imp.transfer == common.TransferAttach {
			return nil, fmt.Errorf("direct attachment of image %q: %w", image.ID, err)
		}
		log.Printf("openstack-instance: image %q: %v; reading it through Glance", image.ID, err)
		return imp.client.NewGlanceDisk(ctx, image, diskMetadata, cleanup), nil
	}
	return disk, nil
}

// readVolumeDisk reads the clone tmp of an attached volume one of two ways.
// Through host, when there is one, it is attached to that server and read
// raw from the block device it turns up as. Without one, or when the attach
// fails and the transfer allows it, it is uploaded to a Glance image and
// read from there.
func (imp *Importer) readVolumeDisk(ctx context.Context, host *servers.Server, tmp *volumes.Volume, metadata *common.DiskMetadata, cleanup *common.Cleanup) (*common.Disk, error) {
	if host == nil {
		return imp.glanceDisk(ctx, tmp, metadata, cleanup)
	}
	// NewCinderDisk fails before any of the clone's bytes are read, with
	// the clone detached again.
	disk, err := imp.client.NewCinderDisk(ctx, host, tmp, metadata, cleanup)
	if err != nil {
		if imp.transfer == common.TransferAttach {
			return nil, fmt.Errorf("direct attachment of volume %q: %w", metadata.Volume.ID, err)
		}
		log.Printf("openstack-instance: volume %q: %v; reading it through Glance", metadata.Volume.ID, err)
		return imp.glanceDisk(ctx, tmp, metadata, cleanup)
	}
	return disk, nil
}

// glanceDisk uploads the clone tmp to a Glance image and returns the disk
// read from it.
func (imp *Importer) glanceDisk(ctx context.Context, tmp *volumes.Volume, metadata *common.DiskMetadata, cleanup *common.Cleanup) (*common.Disk, error) {
	img, err := imp.client.UploadVolumeImage(ctx, tmp, cleanup)
	if err != nil {
		return nil, err
	}
	return imp.client.NewGlanceDisk(ctx, img, metadata, cleanup), nil
}

// emitDisk emits dir itself, then its .METADATA.json and its disk.qcow2,
// read from reader.
func (imp *Importer) emitDisk(ctx context.Context, records chan<- *connectors.Record, dir string, metadata *common.DiskMetadata, reader func() (io.ReadCloser, error)) error {
	if err := imp.emitDir(ctx, records, dir); err != nil {
		return err
	}
	if err := imp.emit(ctx, records, dir+"/"+instance.MetadataName, common.JSONReader(metadata)); err != nil {
		return err
	}
	return imp.emit(ctx, records, dir+"/"+block.DiskName("disk", common.DiskFormat(metadata)), reader)
}

// emitDir emits a directory record. Kloset needs one for every directory a
// file's path crosses; it doesn't infer them from file paths alone.
func (imp *Importer) emitDir(ctx context.Context, records chan<- *connectors.Record, pathname string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case records <- connectors.NewRecord(pathname, "", objects.FileInfo{
		Lname:    path.Base(pathname),
		Lmode:    os.ModeDir | 0700,
		LmodTime: time.Unix(0, 0),
	}, nil, nil):
	}
	return nil
}

func (imp *Importer) emit(ctx context.Context, records chan<- *connectors.Record, pathname string, readerFunc func() (io.ReadCloser, error)) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case records <- connectors.NewRecord(pathname, "", objects.FileInfo{
		Lname:    path.Base(pathname),
		Lmode:    0600,
		Lsize:    -1,
		LmodTime: time.Unix(0, 0),
	}, nil, readerFunc):
	}
	return nil
}

func (imp *Importer) addCleanup() *common.Cleanup {
	imp.mu.Lock()
	defer imp.mu.Unlock()
	c := &common.Cleanup{}
	imp.cleanups = append(imp.cleanups, c)
	return c
}

// Close deletes what the backup left: every Cleanup, in any order.
func (imp *Importer) Close(ctx context.Context) error {
	imp.mu.Lock()
	cleanups := imp.cleanups
	imp.mu.Unlock()
	for _, c := range cleanups {
		c.Run(ctx)
	}
	return nil
}
