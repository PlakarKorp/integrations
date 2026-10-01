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

	"github.com/PlakarKorp/integrations/openstack/block"
	"github.com/PlakarKorp/integrations/openstack/common"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
)

//go:embed schema.json
var ExporterSchema []byte

type Exporter struct {
	client *common.Client
	// imageID is the restore image, until the volume is created from it.
	imageID string
}

// NewExporter restores into a new volume, so the location names nothing:
// it must be proto:// alone.
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
// order, so /.METADATA.json arrives before /<volume-id>.qcow2.
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
			// A second disk would leak the first one's restore image.
			if exp.imageID != "" {
				err := fmt.Errorf("%s: a second disk", record.Pathname)
				results <- record.Error(err)
				return err
			}
			id, err := uploadDisk(ctx, exp.client, record, volumeID, metadata)
			if err != nil {
				results <- record.Error(err)
				return err
			}
			exp.imageID = id
			results <- record.Ok()

		default:
			results <- record.Error(fmt.Errorf("unsupported path %q", record.Pathname))
		}
	}

	if exp.imageID == "" {
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
// result. Image properties are not restored.
func uploadDisk(ctx context.Context, client *common.Client, record *connectors.Record, volumeID string, metadata *common.DiskMetadata) (string, error) {
	if metadata == nil {
		return "", fmt.Errorf("%s before %s", record.Pathname, block.MetadataName)
	}
	if volumeID != metadata.Volume.ID {
		return "", fmt.Errorf("volume ID %q in path %q does not match metadata %q", volumeID, record.Pathname, metadata.Volume.ID)
	}

	image, err := client.UploadTempImage(ctx, common.RestorePrefix+volumeID, record.Reader, diskFormat(metadata))
	if err != nil {
		return "", fmt.Errorf("upload disk %q: %w", volumeID, err)
	}
	log.Printf("openstack-block: uploaded restore image %q for volume %q", image.ID, volumeID)
	return image.ID, nil
}

func diskFormat(metadata *common.DiskMetadata) string {
	if metadata.Image == nil || metadata.Image.DiskFormat == "" {
		return common.DefaultDiskFormat
	}
	return metadata.Image.DiskFormat
}
