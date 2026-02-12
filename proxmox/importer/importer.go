/*
 * Copyright (c) 2026 Gilles DUBOIS
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
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/gillesdubois/plakar-integration-proxmox/internal/proxmox"
)

type ProxmoxImporter struct {
	cfg       *proxmox.Config
	client    *proxmox.Client
	selection selection
}

type selection struct {
	vmid *int
	pool string
	all  bool
}

func init() {
	if err := importer.Register("proxmox", 0, NewProxmoxImporter); err != nil {
		panic(err)
	}
}

func NewProxmoxImporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	cfg, err := proxmox.ParseConfig(config)
	if err != nil {
		return nil, err
	}

	selection, err := parseSelection(config)
	if err != nil {
		return nil, err
	}

	client, err := proxmox.NewClient(cfg)
	if err != nil {
		return nil, err
	}

	return &ProxmoxImporter{
		cfg:       cfg,
		client:    client,
		selection: selection,
	}, nil
}

func (p *ProxmoxImporter) Origin() string        { return p.cfg.Origin() }
func (p *ProxmoxImporter) Type() string          { return "proxmox" }
func (p *ProxmoxImporter) Root() string          { return "/" }
func (p *ProxmoxImporter) Flags() location.Flags { return location.FLAG_STREAM | location.FLAG_NEEDACK }

func (p *ProxmoxImporter) Ping(ctx context.Context) error {
	return p.client.Ping(ctx)
}

func (p *ProxmoxImporter) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	vmids, err := p.resolveVMIDs(ctx)
	if err != nil {
		return err
	}
	if len(vmids) == 0 {
		return fmt.Errorf("no VM/CT found for selection")
	}

	for _, vmid := range vmids {
		if err := ctx.Err(); err != nil {
			return err
		}

		archivePath, reader, sizePtr, err := p.client.BackupVMStream(ctx, vmid)
		if err != nil {
			return err
		}

		archiveName := path.Base(archivePath)
		if archiveName == "" {
			return fmt.Errorf("empty archive name for vmid %d", vmid)
		}
		pathname := "/" + archiveName
		modTime := time.Now()
		fileInfo := objects.FileInfo{
			Lname:    archiveName,
			Lsize:    0,
			Lmode:    0600,
			LmodTime: modTime,
			Ldev:     1,
		}

		record := &connectors.Record{
			Pathname: pathname,
			Target:   "",
			FileInfo: fileInfo,
			Reader:   reader,
		}
		records <- record

		if results != nil {
			ack, ok := <-results
			if ok && ack.Err != nil {
				return ack.Err
			}
		}

		meta := proxmox.NewDumpMetadata(p.cfg, vmid, archiveName, nil)
		if sizePtr != nil {
			meta.ArchiveSize = *sizePtr
		}
		meta.CreatedAt = modTime
		payload, err := proxmox.EncodeDumpMetadata(meta)
		if err != nil {
			return err
		}

		metaPath := "/" + proxmox.MetadataFilename(archiveName)
		metaInfo := objects.FileInfo{
			Lname:    path.Base(metaPath),
			Lsize:    int64(len(payload)),
			Lmode:    0644,
			LmodTime: modTime,
			Ldev:     1,
		}

		metaRecord := connectors.NewRecord(metaPath, "", metaInfo, nil, func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(payload)), nil
		})
		records <- metaRecord

		if results != nil {
			ack, ok := <-results
			if ok && ack.Err != nil {
				return ack.Err
			}
		}

		if p.cfg.Cleanup && archivePath != "" && path.IsAbs(archivePath) {
			if err := p.client.Remove(ctx, archivePath); err != nil {
				return err
			}
		}
	}

	return nil
}

func (p *ProxmoxImporter) Close(ctx context.Context) error {
	return p.client.Close()
}

func (p *ProxmoxImporter) resolveVMIDs(ctx context.Context) ([]int, error) {
	switch {
	case p.selection.vmid != nil:
		return []int{*p.selection.vmid}, nil
	case p.selection.pool != "":
		return p.client.ListPoolVMIDs(ctx, p.selection.pool)
	case p.selection.all:
		return p.client.ListAllVMIDs(ctx)
	default:
		return nil, fmt.Errorf("missing backup selection: vmid, pool or all")
	}
}

func parseSelection(config map[string]string) (selection, error) {
	var sel selection

	if vmidStr, ok := config["vmid"]; ok {
		vmidStr = strings.TrimSpace(vmidStr)
		if vmidStr != "" {
			vmid, err := strconv.Atoi(vmidStr)
			if err != nil {
				return sel, fmt.Errorf("invalid vmid: %s", vmidStr)
			}
			sel.vmid = &vmid
		}
	}

	if pool, ok := config["pool"]; ok {
		pool = strings.TrimSpace(pool)
		if pool != "" {
			sel.pool = pool
		}
	}

	if all, ok := config["all"]; ok {
		all = strings.TrimSpace(all)
		if all == "" || strings.EqualFold(all, "true") || all == "1" || strings.EqualFold(all, "yes") {
			sel.all = true
		}
	}

	setCount := 0
	if sel.vmid != nil {
		setCount++
	}
	if sel.pool != "" {
		setCount++
	}
	if sel.all {
		setCount++
	}

	if setCount == 0 {
		return sel, nil
	}
	if setCount > 1 {
		return sel, fmt.Errorf("backup selection must specify only one of vmid, pool or all")
	}

	return sel, nil
}
