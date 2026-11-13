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
	"github.com/PlakarKorp/integrations/openstack/instance"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
)

//go:embed schema.json
var ImporterSchema []byte

type Importer struct {
	client   *common.Client
	serverID string
	// transfer is the openstack_transfer option: how every disk's bytes
	// reach plakar.
	transfer common.Transfer

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
	clients, err := common.Connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if len(clients) != 1 {
		return nil, fmt.Errorf("connect: expected one region, got %d", len(clients))
	}
	return &Importer{client: clients[0], serverID: serverID, transfer: cfg.Transfer}, nil
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

	// Resolve the transfer before any image or snapshot: one that cannot
	// be taken fails the backup with nothing to clean up.
	resolved, err := imp.client.ResolveTransfer(ctx, imp.transfer)
	if err != nil {
		return err
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
	root, err := imp.readRootDisk(ctx, resolved, metadata, image, rootCleanup)
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
		disk, err := imp.client.ReadCinderDisk(ctx, resolved, vol, snap, tmp, cleanup)
		if err != nil {
			return err
		}
		if err := imp.emitDisk(ctx, records, instance.VolumeDir(av.ID), disk.Metadata, disk.Reader); err != nil {
			return err
		}
	}
	return nil
}

// readRootDisk reads image as a temporary Cinder volume attached to the
// resolved host when there is one, else downloads it from Glance. A volume the
// cloud will not make or attach goes through Glance under auto, and fails
// under attach; whatever was made is in cleanup either way.
func (imp *Importer) readRootDisk(ctx context.Context, resolved common.ResolvedTransfer, metadata *common.ServerMetadata, image *images.Image, cleanup *common.Cleanup) (*common.Disk, error) {
	diskMetadata := common.GlanceDiskMetadata(image)
	if resolved.Host != nil {
		disk, err := imp.attachRootDisk(ctx, resolved.Host, metadata, image, diskMetadata, cleanup)
		switch {
		case err == nil:
			return disk, nil
		case resolved.Transfer == common.TransferAttach:
			return nil, fmt.Errorf("openstack_transfer=attach: image %q: %w", image.ID, err)
		default:
			log.Printf("openstack-instance: image %q: %v; reading it through Glance", image.ID, err)
		}
	}
	return imp.client.NewGlanceDisk(ctx, image, diskMetadata, cleanup), nil
}

// attachRootDisk materializes image as a temporary volume in the server's
// availability zone and reads it from an attachment to host.
func (imp *Importer) attachRootDisk(ctx context.Context, host *servers.Server, metadata *common.ServerMetadata, image *images.Image, diskMetadata *common.DiskMetadata, cleanup *common.Cleanup) (*common.Disk, error) {
	tmp, err := imp.client.CreateTempVolumeFromImage(ctx, image.ID, common.TempVolumeOpts(metadata.Server, metadata), cleanup)
	if err != nil {
		return nil, err
	}
	return imp.client.NewCinderDisk(ctx, host, tmp, diskMetadata, cleanup)
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
		LmodTime: time.Now(),
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
