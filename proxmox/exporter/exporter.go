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

package exporter

import (
	"context"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
	"github.com/gillesdubois/plakar-integration-proxmox/internal/proxmox"
)

type ProxmoxExporter struct {
	cfg    *proxmox.Config
	client *proxmox.Client
}

func init() {
	if err := exporter.Register("proxmox", 0, NewProxmoxExporter); err != nil {
		panic(err)
	}
}

func NewProxmoxExporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	cfg, err := proxmox.ParseConfig(config)
	if err != nil {
		return nil, err
	}

	client, err := proxmox.NewClient(cfg)
	if err != nil {
		return nil, err
	}

	return &ProxmoxExporter{cfg: cfg, client: client}, nil
}

func (p *ProxmoxExporter) Origin() string        { return p.cfg.Origin() }
func (p *ProxmoxExporter) Type() string          { return "proxmox" }
func (p *ProxmoxExporter) Root() string          { return "/" }
func (p *ProxmoxExporter) Flags() location.Flags { return 0 }

func (p *ProxmoxExporter) Ping(ctx context.Context) error {
	return p.client.Ping(ctx)
}

func (p *ProxmoxExporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	for record := range records {
		if record.Err != nil || record.IsXattr || !record.FileInfo.Lmode.IsRegular() {
			results <- record.Ok()
			continue
		}

		base := path.Base(record.Pathname)
		vmType, vmid, err := proxmox.ParseDumpFilename(base)
		if err != nil {
			if strings.HasPrefix(base, "vzdump-") {
				results <- record.Error(err)
				continue
			}
			results <- record.Ok()
			continue
		}
		dumpName := proxmox.BuildRestoreDumpFilename(base, vmType, vmid, time.Now())
		dumpPath := path.Join(p.cfg.DumpDir, dumpName)

		if err := p.writeDump(ctx, dumpPath, record.Reader); err != nil {
			results <- record.Error(err)
			continue
		}

		if err := closeRecord(record); err != nil {
			results <- resultFromRecord(record, err)
			continue
		}

		if err := p.restoreDump(ctx, dumpPath, vmType, vmid); err != nil {
			results <- resultFromRecord(record, err)
			continue
		}

		if p.cfg.Cleanup {
			if err := p.client.Remove(ctx, dumpPath); err != nil {
				results <- resultFromRecord(record, err)
				continue
			}
		}

		results <- resultFromRecord(record, nil)
	}

	return nil
}

func (p *ProxmoxExporter) Close(ctx context.Context) error {
	return p.client.Close()
}

func (p *ProxmoxExporter) writeDump(ctx context.Context, dumpPath string, reader io.Reader) error {
	writer, err := p.client.Create(ctx, dumpPath)
	if err != nil {
		return err
	}

	if _, err := io.Copy(writer, reader); err != nil {
		_ = writer.Close()
		return err
	}
	return writer.Close()
}

func (p *ProxmoxExporter) restoreDump(ctx context.Context, dumpPath, vmType string, vmid int) error {
	if err := p.stopVM(ctx, vmType, vmid); err != nil {
		return err
	}

	vmidStr := strconv.Itoa(vmid)
	var cmd string
	var args []string

	switch vmType {
	case "qemu":
		cmd = "qmrestore"
		args = []string{dumpPath, vmidStr, "--force"}
	case "lxc":
		cmd = "pct"
		args = []string{"restore", vmidStr, dumpPath, "--force"}
	default:
		return fmt.Errorf("unsupported backup type: %s", vmType)
	}

	_, stderr, err := p.client.Run(ctx, cmd, args...)
	if err != nil {
		return fmt.Errorf("restore failed: %w: %s", err, strings.TrimSpace(stderr))
	}

	return nil
}

func (p *ProxmoxExporter) stopVM(ctx context.Context, vmType string, vmid int) error {
	vmidStr := strconv.Itoa(vmid)
	var cmd string

	switch vmType {
	case "qemu":
		cmd = "qm"
	case "lxc":
		cmd = "pct"
	default:
		return fmt.Errorf("unsupported backup type: %s", vmType)
	}

	stdout, stderr, err := p.client.Run(ctx, cmd, "stop", vmidStr)
	if err != nil {
		output := strings.TrimSpace(stderr)
		if output == "" {
			output = strings.TrimSpace(stdout)
		}
		if isIgnorableStopError(output) {
			return nil
		}
		return fmt.Errorf("stop failed for %s %d: %w: %s", vmType, vmid, err, output)
	}

	return nil
}

func isIgnorableStopError(output string) bool {
	if output == "" {
		return false
	}
	normalized := strings.ToLower(output)
	return strings.Contains(normalized, "not running") ||
		strings.Contains(normalized, "already stopped") ||
		strings.Contains(normalized, "does not exist") ||
		strings.Contains(normalized, "no such vm") ||
		strings.Contains(normalized, "no such container") ||
		strings.Contains(normalized, "configuration file")
}

func closeRecord(record *connectors.Record) error {
	if record.Reader == nil {
		return nil
	}
	err := record.Close()
	record.Reader = nil
	return err
}

func resultFromRecord(record *connectors.Record, err error) *connectors.Result {
	return &connectors.Result{
		Record: *record,
		Err:    err,
	}
}
