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

const protocolName = "proxmox+backup"
const backupSnapshotRoot = "/backup"

func init() {
	if err := importer.Register(protocolName, 0, NewProxmoxImporter); err != nil {
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
func (p *ProxmoxImporter) Type() string          { return protocolName }
func (p *ProxmoxImporter) Root() string          { return "/" }
func (p *ProxmoxImporter) Flags() location.Flags { return location.FLAG_STREAM }

func (p *ProxmoxImporter) Ping(ctx context.Context) error {
	return p.client.Ping(ctx)
}

func (p *ProxmoxImporter) Import(ctx context.Context, records chan<- *connectors.Record, _ <-chan *connectors.Result) error {
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

		vmType, err := p.client.VMType(ctx, vmid)
		if err != nil {
			return err
		}

		vmName, err := p.client.VMName(ctx, vmid)
		if err != nil {
			return err
		}

		backupRecord, err := p.buildBackupRecord(ctx, vmType, vmid, vmName)
		if err != nil {
			return err
		}

		archivePath := backupRecord.archivePath
		archiveName := path.Base(archivePath)
		if isInvalidArchiveName(archiveName) {
			_ = backupRecord.record.Close()
			return fmt.Errorf("invalid archive name for vmid %d: %q", vmid, archiveName)
		}

		if err := p.emitRecord(ctx, records, backupRecord.record); err != nil {
			return err
		}

		if vmType == "qemu" || vmType == "lxc" {
			if err := p.emitVMConfigRecord(ctx, records, vmType, vmid, vmName, archiveName); err != nil {
				return err
			}
			if err := p.emitVMPoolRecord(ctx, records, vmType, vmid, vmName, archiveName); err != nil {
				return err
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

type backupRecord struct {
	archivePath string
	record      *connectors.Record
}

func (p *ProxmoxImporter) buildBackupRecord(ctx context.Context, vmType string, vmid int, vmName string) (*backupRecord, error) {
	archivePath, err := p.client.BackupVM(ctx, vmid)
	if err != nil {
		return nil, err
	}

	fileInfo, err := p.client.Stat(ctx, archivePath)
	if err != nil {
		return nil, err
	}

	reader, err := p.client.Open(ctx, archivePath)
	if err != nil {
		return nil, err
	}

	archiveName := path.Base(archivePath)
	if isInvalidArchiveName(archiveName) {
		_ = reader.Close()
		return nil, fmt.Errorf("invalid archive name for vmid %d: %q", vmid, archiveName)
	}

	return &backupRecord{
		archivePath: archivePath,
		record: &connectors.Record{
			Pathname: buildBackupSnapshotPath(vmType, vmid, vmName, archiveName),
			FileInfo: objects.FileInfo{
				Lname:    archiveName,
				Lsize:    fileInfo.Size(),
				Lmode:    0600,
				LmodTime: fileInfo.ModTime(),
				Ldev:     1,
			},
			Reader: reader,
		},
	}, nil
}

func (p *ProxmoxImporter) emitVMConfigRecord(ctx context.Context, records chan<- *connectors.Record, vmType string, vmid int, vmName, archiveName string) error {
	var (
		configData []byte
		configName string
		err        error
	)

	switch vmType {
	case "qemu":
		configData, err = p.client.ReadQEMUConfig(ctx, vmid)
		configName = proxmox.BuildQEMUConfigSidecarFilename(archiveName)
	case "lxc":
		configData, err = p.client.ReadLXCConfig(ctx, vmid)
		configName = proxmox.BuildLXCConfigSidecarFilename(archiveName)
	default:
		return nil
	}
	if err != nil {
		return err
	}

	record := &connectors.Record{
		Pathname: buildBackupSnapshotPath(vmType, vmid, vmName, configName),
		FileInfo: objects.FileInfo{
			Lname:    configName,
			Lsize:    int64(len(configData)),
			Lmode:    0600,
			LmodTime: time.Now(),
			Ldev:     1,
		},
		Reader: io.NopCloser(bytes.NewReader(configData)),
	}

	return p.emitRecord(ctx, records, record)
}

func (p *ProxmoxImporter) emitVMPoolRecord(ctx context.Context, records chan<- *connectors.Record, vmType string, vmid int, vmName, archiveName string) error {
	poolName, err := p.client.VMPool(ctx, vmid)
	if err != nil {
		return err
	}
	poolName = strings.TrimSpace(poolName)
	if poolName == "" {
		return nil
	}

	poolSidecarName := proxmox.BuildPoolSidecarFilename(archiveName)
	poolData := []byte(poolName)

	record := &connectors.Record{
		Pathname: buildBackupSnapshotPath(vmType, vmid, vmName, poolSidecarName),
		FileInfo: objects.FileInfo{
			Lname:    poolSidecarName,
			Lsize:    int64(len(poolData)),
			Lmode:    0600,
			LmodTime: time.Now(),
			Ldev:     1,
		},
		Reader: io.NopCloser(bytes.NewReader(poolData)),
	}

	return p.emitRecord(ctx, records, record)
}

func (p *ProxmoxImporter) emitRecord(ctx context.Context, records chan<- *connectors.Record, record *connectors.Record) error {
	select {
	case <-ctx.Done():
		_ = record.Close()
		return ctx.Err()
	case records <- record:
	}
	return nil
}

func isInvalidArchiveName(name string) bool {
	return name == "" || name == "." || name == "/"
}

func buildBackupSnapshotPath(vmType string, vmid int, vmName, filename string) string {
	return path.Join(backupSnapshotRoot, vmType, buildBackupSnapshotDir(vmid, vmName), filename)
}

func buildBackupSnapshotDir(vmid int, vmName string) string {
	name := sanitizeSnapshotDirComponent(vmName)
	if name == "" {
		name = "unnamed"
	}
	return fmt.Sprintf("%d_%s", vmid, name)
}

func sanitizeSnapshotDirComponent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(value))

	lastUnderscore := false
	for _, r := range value {
		allowed := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '.'

		if allowed {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}

		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}

	return strings.Trim(b.String(), "._-")
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
