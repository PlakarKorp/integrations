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

	"github.com/PlakarKorp/integrations/openstack/block"
	"github.com/PlakarKorp/integrations/openstack/common"
	"github.com/PlakarKorp/integrations/openstack/common/attach"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
)

//go:embed schema.json
var ExporterSchema []byte

type Exporter struct {
	client *common.Client
	// imageID is the restore image, until the volume is created from it.
	imageID string
	// volumeID is the restored volume, once the disk was written to it
	// through an attachment instead of going through an image.
	volumeID string
}

// NewExporter restores into a new, detached volume: location is bare
// (proto://) or proto://spawn, both selecting the same, only supported mode.
func NewExporter(ctx context.Context, opts *connectors.Options, proto string, params map[string]string) (exporter.Exporter, error) {
	suffix, ok := strings.CutPrefix(params["location"], proto+"://")
	switch {
	case ok && (suffix == "" || suffix == common.SpawnLocation):
	case ok && common.IDFormat.MatchString(suffix):
		return nil, fmt.Errorf("location: restoring onto an existing resource (%q) is not supported yet", suffix)
	default:
		return nil, fmt.Errorf("location: bad value: %q is not %s://, %s://spawn, or %s://<id>", params["location"], proto, proto, proto)
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
	return &Exporter{client: clients[0]}, nil
}

func (exp *Exporter) Origin() string        { return "" }
func (exp *Exporter) Type() string          { return block.Protocol }
func (exp *Exporter) Root() string          { return "/" }
func (exp *Exporter) Flags() location.Flags { return 0 }

func (exp *Exporter) Ping(ctx context.Context) error {
	for _, err := range exp.client.ListVolumes(ctx) {
		if err != nil {
			return err
		}
		break
	}
	return nil
}

// Export expects the importer's layout. Kloset walks the snapshot in path
// order, so /.METADATA.json arrives before /<volume-id>.qcow2. The disk is
// restored as it arrives (see restoreDisk); through Glance, the volume is
// created from the image once the records are in.
func (exp *Exporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	var metadata *common.DiskMetadata

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

		volumeID, isDisk := block.VolumeID(record.Pathname)
		switch {
		case record.FileInfo.IsDir():
			results <- record.Ok()

		case record.Pathname == "/"+block.MetadataName:
			if metadata != nil {
				err := fmt.Errorf("duplicate %s", block.MetadataName)
				results <- record.Error(err)
				return err
			}
			m, err := decodeMetadata(record)
			if err != nil {
				results <- record.Error(err)
				return err
			}
			metadata = m
			results <- record.Ok()

		case isDisk:
			// A second disk would leak the first one's restore image, or
			// restore over the first one's volume.
			if exp.imageID != "" || exp.volumeID != "" {
				err := fmt.Errorf("%s: a second disk", record.Pathname)
				results <- record.Error(err)
				return err
			}
			if err := exp.restoreDisk(ctx, record, volumeID, metadata); err != nil {
				results <- record.Error(err)
				return err
			}
			results <- record.Ok()

		default:
			results <- record.Error(fmt.Errorf("unsupported path %q", record.Pathname))
		}
	}

	switch {
	case exp.volumeID != "":
		return nil
	case exp.imageID == "":
		return errors.New("the snapshot has no disk")
	}

	vol, err := exp.client.CreateVolumeFromImage(ctx, exp.imageID, metadata.Volume)
	if err != nil {
		return fmt.Errorf("restore volume %q: %w", metadata.Volume.ID, err)
	}
	log.Printf("openstack-block: restored volume %q from image %q", vol.ID, exp.imageID)

	exp.client.DeleteTempImage(ctx, exp.imageID)
	exp.imageID = ""
	return nil
}

// restoreDisk checks the disk against the metadata, then restores it one of
// two ways. A raw disk, when plakar runs on a Nova server of the cloud, is
// written to a new volume attached to that server: exp.volumeID is then the
// restored volume, done. Otherwise, a qcow2 disk included, it is uploaded to
// a Glance image, exp.imageID, for the volume to be created from once the
// records are in.
func (exp *Exporter) restoreDisk(ctx context.Context, record *connectors.Record, volumeID string, metadata *common.DiskMetadata) error {
	if metadata == nil {
		return fmt.Errorf("%s before %s", record.Pathname, block.MetadataName)
	}
	if volumeID != metadata.Volume.ID {
		return fmt.Errorf("volume ID %q in path %q does not match metadata %q", volumeID, record.Pathname, metadata.Volume.ID)
	}

	if common.ValidateWriteAttachment(metadata) == nil {
		vol, err := writeDisk(ctx, exp.client, record, metadata)
		if err != nil {
			return err
		}
		if vol != nil {
			exp.volumeID = vol.ID
			return nil
		}
	}

	id, err := uploadDisk(ctx, exp.client, record, volumeID, metadata)
	if err != nil {
		return err
	}
	exp.imageID = id
	return nil
}

// writeDisk restores a raw disk through an attachment to the host, when the
// host is a Nova server of the cloud and the attach works, and returns the
// restored volume. It returns nil, nil when the disk is better sent through
// Glance: that is found out before any of the disk's bytes are read. A
// failure once they are is the restore's.
func writeDisk(ctx context.Context, client *common.Client, record *connectors.Record, metadata *common.DiskMetadata) (*volumes.Volume, error) {
	host, err := client.PreflightHost(ctx)
	if err != nil {
		if !errors.Is(err, attach.ErrNotAttachable) {
			log.Printf("openstack-block: direct attachment unavailable: %v", err)
		}
		return nil, nil
	}
	w, err := client.AttachRestoreVolume(ctx, host, metadata)
	if err != nil {
		log.Printf("openstack-block: volume %q: %v; restoring it through Glance", metadata.Volume.ID, err)
		return nil, nil
	}
	log.Printf("openstack-block: writing volume %q to new volume %q through server %q", metadata.Volume.ID, w.Volume().ID, host.ID)
	vol, err := w.Copy(ctx, record.Reader)
	if err != nil {
		return nil, fmt.Errorf("write disk %q: %w", metadata.Volume.ID, err)
	}
	log.Printf("openstack-block: restored volume %q by writing it", vol.ID)
	return vol, nil
}

// Close deletes the restore image a failed restore left behind.
func (exp *Exporter) Close(ctx context.Context) error {
	if exp.imageID != "" {
		exp.client.DeleteTempImage(ctx, exp.imageID)
		exp.imageID = ""
	}
	return nil
}

// decodeMetadata requires the volume's ID and size: the volume is recreated
// from them.
func decodeMetadata(record *connectors.Record) (*common.DiskMetadata, error) {
	m := new(common.DiskMetadata)
	if err := json.NewDecoder(record.Reader).Decode(m); err != nil {
		return nil, fmt.Errorf("decode %s: %w", block.MetadataName, err)
	}
	switch {
	case m.Volume == nil:
		return nil, fmt.Errorf("%s: no volume", block.MetadataName)
	case m.Volume.ID == "":
		return nil, fmt.Errorf("%s: no volume ID", block.MetadataName)
	case m.Volume.Size <= 0:
		return nil, fmt.Errorf("%s: volume %q has no size", block.MetadataName, m.Volume.ID)
	}
	return m, nil
}

// uploadDisk uploads inline, so the record's answer reports the upload's
// result. Only the image properties a restore needs, e.g.
// cinder_encryption_key_id, are carried over; the rest are not.
func uploadDisk(ctx context.Context, client *common.Client, record *connectors.Record, volumeID string, metadata *common.DiskMetadata) (string, error) {
	image, err := client.UploadTempImage(ctx, common.RestorePrefix+volumeID, record.Reader, common.DiskFormat(metadata), common.RestoreImageProperties(metadata))
	if err != nil {
		return "", fmt.Errorf("upload disk %q: %w", volumeID, err)
	}
	log.Printf("openstack-block: uploaded restore image %q for volume %q", image.ID, volumeID)
	return image.ID, nil
}
