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
	"strings"
	"time"

	"github.com/PlakarKorp/integrations/openstack/block"
	"github.com/PlakarKorp/integrations/openstack/common"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

//go:embed schema.json
var ImporterSchema []byte

type Importer struct {
	client   *common.Client
	volumeID string
	// cleanup holds the snapshot, temporary volume and image a backup
	// creates. Closing the disk reader runs it; Close runs whatever is left,
	// for when the reader was never opened.
	cleanup common.Cleanup
}

func NewImporter(ctx context.Context, opts *connectors.Options, proto string, params map[string]string) (importer.Importer, error) {
	volumeID := strings.TrimPrefix(params["location"], proto+"://")
	if !common.IDFormat.MatchString(volumeID) {
		return nil, fmt.Errorf("location: bad value: %q is not %s://<volume-id>", params["location"], proto)
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
	return &Importer{client: clients[0], volumeID: volumeID}, nil
}

func (imp *Importer) Origin() string { return imp.volumeID }
func (imp *Importer) Type() string   { return block.Protocol }
func (imp *Importer) Root() string   { return "/" }

// FLAG_STREAM: Import is not safe to call twice, it creates a real snapshot.
func (imp *Importer) Flags() location.Flags { return location.FLAG_STREAM }

func (imp *Importer) Ping(ctx context.Context) error {
	_, err := imp.client.GetVolume(ctx, imp.volumeID)
	return err
}

// Import emits /<volume-id>.qcow2 and /.METADATA.json. The disk keeps its
// .qcow2 name whatever its format; the metadata's image carries the real one.
func (imp *Importer) Import(ctx context.Context, records chan<- *connectors.Record, _ <-chan *connectors.Result) error {
	defer close(records)

	// Snapshot the volume and turn the snapshot into a Glance image. The
	// temporary resources go into imp.cleanup, for Close to delete on failure.
	vi, err := imp.client.CreateVolumeImage(ctx, imp.volumeID, &imp.cleanup)
	if err != nil {
		return err
	}

	// Emit the disk. Plakar downloads it when it reads the record, and closing
	// the download deletes the temporary resources.
	disk := block.DiskName(imp.volumeID)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case records <- connectors.NewRecord("/"+disk, "", objects.FileInfo{
		Lname:    disk,
		Lmode:    0600,
		Lsize:    -1,
		LmodTime: time.Unix(0, 0),
	}, nil, imp.client.ImageReader(ctx, vi.Image.ID, &imp.cleanup)):
	}

	// Emit what restore needs to recreate the volume, including the image's real
	// disk format.
	metadata := &common.DiskMetadata{
		Origin:          common.DiskOriginCinder,
		Image:           vi.Image,
		ImageProperties: common.EncryptionImageProperties(vi.Image),
		Volume:          vi.Volume,
		Snapshot:        vi.Snapshot,
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case records <- connectors.NewRecord("/"+block.MetadataName, "", objects.FileInfo{
		Lname:    block.MetadataName,
		Lmode:    0600,
		Lsize:    -1,
		LmodTime: time.Unix(0, 0),
	}, nil, common.JSONReader(metadata)):
	}
	return nil
}

func (imp *Importer) Close(ctx context.Context) error {
	imp.cleanup.Run(ctx)
	return nil
}
