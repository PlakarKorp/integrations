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
	"errors"
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
	cfg         *proxmox.Config
	client      *proxmox.Client
	restoreOpts restoreOptions
}

type vmConfigSidecar struct {
	vmType string
	data   []byte
}

type pendingRestore struct {
	record   *connectors.Record
	vmType   string
	vmid     int
	dumpBase string
	dumpPath string
}

type vmRuntimeState struct {
	exists  bool
	running bool
}

type restoreOptions struct {
	startOnRestore bool
	forceVMRestore bool
	newID          int
	storage        string
	pool           string
}

const protocolName = "proxmox+backup"

func init() {
	if err := exporter.Register(protocolName, 0, NewProxmoxExporter); err != nil {
		panic(err)
	}
}

func NewProxmoxExporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	cfg, err := proxmox.ParseConfig(config)
	if err != nil {
		return nil, err
	}

	restoreOpts, err := parseRestoreOptions(config)
	if err != nil {
		return nil, err
	}

	client, err := proxmox.NewClient(cfg)
	if err != nil {
		return nil, err
	}

	return &ProxmoxExporter{
		cfg:         cfg,
		client:      client,
		restoreOpts: restoreOpts,
	}, nil
}

func (p *ProxmoxExporter) Origin() string        { return p.cfg.Origin() }
func (p *ProxmoxExporter) Type() string          { return protocolName }
func (p *ProxmoxExporter) Root() string          { return "/" }
func (p *ProxmoxExporter) Flags() location.Flags { return 0 }

func (p *ProxmoxExporter) Ping(ctx context.Context) error {
	return p.client.Ping(ctx)
}

func (p *ProxmoxExporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	sidecars := make(map[string]vmConfigSidecar)
	poolSidecars := make(map[string]string)
	pendingRestores := make([]pendingRestore, 0)

	for record := range records {
		if err := ctx.Err(); err != nil {
			results <- record.Error(err)
			continue
		}

		if record.Err != nil || record.IsXattr || !record.FileInfo.Lmode.IsRegular() {
			results <- record.Ok()
			continue
		}

		base := path.Base(record.Pathname)
		if proxmox.IsConfigSidecarFilename(base) {
			if err := p.collectConfigSidecar(record, base, sidecars); err != nil {
				_ = closeRecord(record)
				results <- resultFromRecord(record, err)
				continue
			}
			results <- resultFromRecord(record, nil)
			continue
		}
		if proxmox.IsPoolSidecarFilename(base) {
			if err := p.collectPoolSidecar(record, base, poolSidecars); err != nil {
				_ = closeRecord(record)
				results <- resultFromRecord(record, err)
				continue
			}
			results <- resultFromRecord(record, nil)
			continue
		}

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

		pendingRestores = append(pendingRestores, pendingRestore{
			record:   record,
			vmType:   vmType,
			vmid:     vmid,
			dumpBase: base,
			dumpPath: dumpPath,
		})
	}

	for _, pending := range pendingRestores {
		if err := ctx.Err(); err != nil {
			results <- resultFromRecord(pending.record, err)
			continue
		}

		configData, err := p.resolveConfigForDump(pending, sidecars)
		if err == nil {
			poolName, poolErr := p.resolvePoolForDump(pending, poolSidecars)
			if poolErr != nil {
				err = poolErr
			} else {
				targetVMID := pending.vmid
				if p.restoreOpts.newID != 0 {
					targetVMID = p.restoreOpts.newID
				}

				err = p.restoreDump(ctx, pending.dumpPath, pending.vmType, targetVMID, configData, poolName)
			}
		}

		if err == nil && p.cfg.Cleanup {
			if removeErr := p.client.Remove(ctx, pending.dumpPath); removeErr != nil {
				err = removeErr
			}
		}

		results <- resultFromRecord(pending.record, err)
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

func (p *ProxmoxExporter) collectConfigSidecar(record *connectors.Record, sidecarBase string, sidecars map[string]vmConfigSidecar) error {
	dumpBase, vmType, err := proxmox.ParseConfigSidecarFilename(sidecarBase)
	if err != nil {
		return err
	}

	configData, err := readRecordBytes(record)
	if err != nil {
		return err
	}

	sidecars[dumpBase] = vmConfigSidecar{
		vmType: vmType,
		data:   configData,
	}
	return nil
}

func (p *ProxmoxExporter) resolveConfigForDump(pending pendingRestore, sidecars map[string]vmConfigSidecar) ([]byte, error) {
	sidecar, ok := sidecars[pending.dumpBase]
	if !ok {
		return nil, nil
	}
	if sidecar.vmType != pending.vmType {
		return nil, fmt.Errorf("config sidecar type mismatch for dump %s: got %s, expected %s", pending.dumpBase, sidecar.vmType, pending.vmType)
	}
	return sidecar.data, nil
}

func (p *ProxmoxExporter) collectPoolSidecar(record *connectors.Record, sidecarBase string, sidecars map[string]string) error {
	dumpBase, err := proxmox.ParsePoolSidecarFilename(sidecarBase)
	if err != nil {
		return err
	}

	poolData, err := readRecordBytes(record)
	if err != nil {
		return err
	}
	sidecars[dumpBase] = strings.TrimSpace(string(poolData))
	return nil
}

func (p *ProxmoxExporter) resolvePoolForDump(pending pendingRestore, sidecars map[string]string) (string, error) {
	poolName, ok := sidecars[pending.dumpBase]
	if !ok {
		return "", nil
	}
	return strings.TrimSpace(poolName), nil
}

func (p *ProxmoxExporter) restoreDump(ctx context.Context, dumpPath, vmType string, vmid int, configData []byte, poolName string) error {
	state, err := p.vmState(ctx, vmType, vmid)
	if err != nil {
		return err
	}

	if state.exists && state.running {
		if !p.restoreOpts.forceVMRestore {
			return fmt.Errorf("refusing restore for %s %d: VM/CT is running (stop it first or user force_vm_restore)", vmType, vmid)
		}
		if err := p.stopVM(ctx, vmType, vmid); err != nil {
			return err
		}
		state, err = p.vmState(ctx, vmType, vmid)
		if err != nil {
			return err
		}
		if state.running {
			return fmt.Errorf("refusing restore for %s %d: VM/CT is still running after stop request", vmType, vmid)
		}
	}

	opts, err := p.resolveRestoreOptions(ctx, vmType, state.exists, configData, poolName)
	if err != nil {
		return err
	}

	if err := p.runRestoreDump(ctx, dumpPath, vmType, vmid, opts); err != nil {
		return err
	}

	if p.restoreOpts.startOnRestore {
		if err := p.startVM(ctx, vmType, vmid); err != nil {
			return err
		}
	}

	return nil
}

func (p *ProxmoxExporter) resolveRestoreOptions(ctx context.Context, vmType string, targetExists bool, configData []byte, poolName string) (restoreOptions, error) {
	opts := p.restoreOpts

	if !targetExists {
		if opts.storage == "" {
			opts.storage = parseStorageFromConfig(vmType, configData)
		}
		if opts.pool == "" && poolName != "" {
			exists, err := p.client.PoolExists(ctx, poolName)
			if err != nil {
				return restoreOptions{}, err
			}
			if exists {
				opts.pool = poolName
			}
		}
	}

	if opts.pool != "" {
		exists, err := p.client.PoolExists(ctx, opts.pool)
		if err != nil {
			return restoreOptions{}, err
		}
		if !exists {
			return restoreOptions{}, fmt.Errorf("restore pool does not exist: %s", opts.pool)
		}
	}

	return opts, nil
}

func (p *ProxmoxExporter) runRestoreDump(ctx context.Context, dumpPath, vmType string, vmid int, opts restoreOptions) error {
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
	if opts.storage != "" {
		args = append(args, "--storage", opts.storage)
	}
	if opts.pool != "" {
		args = append(args, "--pool", opts.pool)
	}

	_, stderr, err := p.client.Run(ctx, cmd, args...)
	if err != nil {
		return fmt.Errorf("restore failed: %w: %s", err, strings.TrimSpace(stderr))
	}

	return nil
}

func (p *ProxmoxExporter) vmState(ctx context.Context, vmType string, vmid int) (vmRuntimeState, error) {
	cmd, err := vmCommand(vmType)
	if err != nil {
		return vmRuntimeState{}, err
	}

	vmidStr := strconv.Itoa(vmid)
	stdout, stderr, err := p.client.Run(ctx, cmd, "status", vmidStr)
	output := preferredOutput(stdout, stderr)
	if err != nil {
		if isMissingVMError(output) {
			return vmRuntimeState{exists: false, running: false}, nil
		}
		return vmRuntimeState{}, fmt.Errorf("status failed for %s %d: %w: %s", vmType, vmid, err, output)
	}

	status := parseStatusValue(stdout + "\n" + stderr)
	switch status {
	case "running", "paused", "suspended":
		return vmRuntimeState{exists: true, running: true}, nil
	case "stopped":
		return vmRuntimeState{exists: true, running: false}, nil
	default:
		return vmRuntimeState{}, fmt.Errorf("unable to parse status for %s %d: %s", vmType, vmid, preferredOutput(stdout, stderr))
	}
}

func (p *ProxmoxExporter) startVM(ctx context.Context, vmType string, vmid int) error {
	cmd, err := vmCommand(vmType)
	if err != nil {
		return err
	}

	vmidStr := strconv.Itoa(vmid)
	stdout, stderr, err := p.client.Run(ctx, cmd, "start", vmidStr)
	if err != nil {
		output := preferredOutput(stdout, stderr)
		if isIgnorableStartError(output) {
			return nil
		}
		return fmt.Errorf("start failed for %s %d: %w: %s", vmType, vmid, err, output)
	}

	return nil
}

func (p *ProxmoxExporter) stopVM(ctx context.Context, vmType string, vmid int) error {
	cmd, err := vmCommand(vmType)
	if err != nil {
		return err
	}

	vmidStr := strconv.Itoa(vmid)
	stdout, stderr, err := p.client.Run(ctx, cmd, "stop", vmidStr)
	if err != nil {
		output := preferredOutput(stdout, stderr)
		if isIgnorableStopError(output) {
			return nil
		}
		return fmt.Errorf("stop failed for %s %d: %w: %s", vmType, vmid, err, output)
	}

	return p.waitUntilVMStopped(ctx, vmType, vmid)
}

func (p *ProxmoxExporter) waitUntilVMStopped(ctx context.Context, vmType string, vmid int) error {
	deadline := time.Now().Add(60 * time.Second)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout while waiting for %s %d to stop", vmType, vmid)
		}

		state, err := p.vmState(ctx, vmType, vmid)
		if err != nil {
			return err
		}
		if !state.running {
			return nil
		}

		time.Sleep(1 * time.Second)
	}
}

func vmCommand(vmType string) (string, error) {
	switch vmType {
	case "qemu":
		return "qm", nil
	case "lxc":
		return "pct", nil
	default:
		return "", fmt.Errorf("unsupported backup type: %s", vmType)
	}
}

func isIgnorableStartError(output string) bool {
	normalized := strings.ToLower(output)
	return strings.Contains(normalized, "already running")
}

func isIgnorableStopError(output string) bool {
	normalized := strings.ToLower(output)
	return strings.Contains(normalized, "already stopped") ||
		strings.Contains(normalized, "already down") ||
		strings.Contains(normalized, "does not exist") ||
		strings.Contains(normalized, "no such vm") ||
		strings.Contains(normalized, "no such container")
}

func parseBoolOption(value string) (bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("invalid boolean value: %s", value)
	}
	return parsed, nil
}

func parseRestoreOptions(config map[string]string) (restoreOptions, error) {
	var opts restoreOptions

	startOnRestore, err := parseBoolOption(config["start_on_restore"])
	if err != nil {
		return restoreOptions{}, err
	}
	opts.startOnRestore = startOnRestore

	forceVMRestore, err := parseBoolOption(config["force_vm_restore"])
	if err != nil {
		return restoreOptions{}, err
	}
	opts.forceVMRestore = forceVMRestore

	opts.storage = strings.TrimSpace(config["storage"])
	opts.pool = strings.TrimSpace(config["pool"])

	newIDRaw, hasNewID := config["newid"]
	if hasNewID {
		newIDRaw = strings.TrimSpace(newIDRaw)
		if newIDRaw != "" {
			newID, err := strconv.Atoi(newIDRaw)
			if err != nil {
				return restoreOptions{}, fmt.Errorf("invalid newid value: %s", newIDRaw)
			}
			if newID <= 0 {
				return restoreOptions{}, fmt.Errorf("newid must be a positive integer: %s", newIDRaw)
			}
			opts.newID = newID
		}
	}

	return opts, nil
}

func isMissingVMError(output string) bool {
	if output == "" {
		return false
	}
	normalized := strings.ToLower(output)
	return strings.Contains(normalized, "does not exist") ||
		strings.Contains(normalized, "no such vm") ||
		strings.Contains(normalized, "no such container") ||
		strings.Contains(normalized, "configuration file")
}

func preferredOutput(stdout, stderr string) string {
	output := strings.TrimSpace(stderr)
	if output == "" {
		output = strings.TrimSpace(stdout)
	}
	return output
}

func parseStatusValue(output string) string {
	for _, line := range strings.Split(strings.ToLower(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "status:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "status:"))
		}
	}
	return ""
}

func parseStorageFromConfig(vmType string, configData []byte) string {
	if len(configData) == 0 {
		return ""
	}

	for _, line := range strings.Split(string(configData), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.ToLower(key))
		value = strings.TrimSpace(value)

		switch vmType {
		case "lxc":
			if key == "rootfs" {
				if storage := parseStorageFromVolumeSpec(value); storage != "" {
					return storage
				}
			}
		case "qemu":
			if !isQEMUDiskConfigKey(key) {
				continue
			}
			if storage := parseStorageFromVolumeSpec(value); storage != "" {
				return storage
			}
		}
	}

	return ""
}

func parseStorageFromVolumeSpec(spec string) string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return ""
	}

	volume := strings.Split(spec, ",")[0]
	volume = strings.TrimSpace(volume)
	if volume == "" {
		return ""
	}

	storage, _, ok := strings.Cut(volume, ":")
	if !ok {
		return ""
	}
	storage = strings.TrimSpace(storage)
	if storage == "" {
		return ""
	}

	// Ignore explicit "none" values used in some optional disk entries.
	if strings.EqualFold(storage, "none") {
		return ""
	}
	return storage
}

func isQEMUDiskConfigKey(key string) bool {
	return strings.HasPrefix(key, "scsi") ||
		strings.HasPrefix(key, "virtio") ||
		strings.HasPrefix(key, "sata") ||
		strings.HasPrefix(key, "ide") ||
		strings.HasPrefix(key, "efidisk") ||
		strings.HasPrefix(key, "tpmstate")
}

func readRecordBytes(record *connectors.Record) ([]byte, error) {
	if record.Reader == nil {
		return nil, fmt.Errorf("missing record reader for %s", record.Pathname)
	}

	data, readErr := io.ReadAll(record.Reader)
	closeErr := closeRecord(record)
	if readErr != nil && closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}

	return data, nil
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
