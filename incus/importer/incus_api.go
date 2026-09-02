/*
 * Copyright (c) 2026 Antoine Dheygers <antoine.dheygers@cryptoweb.fr>
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
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/PlakarKorp/integrations/incus/internal/conn"
	incus "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
	"github.com/lxc/incus/v6/shared/cancel"
)

type incusSource struct {
	server incus.InstanceServer
	// backupTTL is the ExpiresAt horizon of the temporary server-side
	// backup; cleanupTimeout bounds the wait on its deletion. Both come
	// from the config map (backup_ttl, cleanup_timeout), validated in
	// NewImporter.
	backupTTL      time.Duration
	cleanupTimeout time.Duration
}

func newIncusSource(config map[string]string, backupTTL, cleanupTimeout time.Duration) (backupSource, error) {
	// conn.Connect handles the transport (unix socket or HTTPS with
	// client certificate) and the project scoping.
	server, err := conn.Connect(config)
	if err != nil {
		return nil, err
	}
	return &incusSource{server: server, backupTTL: backupTTL, cleanupTimeout: cleanupTimeout}, nil
}

func (s *incusSource) Ping(ctx context.Context) error {
	// GetServer has no ctx-aware variant in the incus client (verified via
	// `go doc`); a frozen daemon can therefore hang this call indefinitely.
	// This is a limitation of the upstream client, not something callable
	// from here.
	_, _, err := s.server.GetServer()
	return err
}

func (s *incusSource) Inspect(ctx context.Context, instance string) (instanceInfo, error) {
	// GetInstance has no ctx-aware variant either (same upstream
	// limitation as GetServer above).
	inst, _, err := s.server.GetInstance(instance)
	if err != nil {
		return instanceInfo{}, err
	}
	return instanceInfo{
		Type:       inst.Type,
		ExtraDisks: extraDiskDevices(inst.ExpandedDevices),
	}, nil
}

// extraDiskDevices returns the names of disk devices other than the
// root disk (path "/"). ExpandedDevices must be used rather than
// Devices: the root disk and attached volumes may come from a profile.
// Those devices are exactly what a backup with InstanceOnly:true does
// NOT include, hence the warning the importer emits from this list.
//
// Virtual disks whose content Incus regenerates from the instance
// config (cloud-init seed ISO, VM agent drive) carry no data to lose
// and would be pure warning noise, so they are skipped.
func extraDiskDevices(devices map[string]map[string]string) []string {
	var names []string
	for name, dev := range devices {
		if dev["type"] != "disk" || dev["path"] == "/" {
			continue
		}
		if dev["source"] == "cloud-init:config" || dev["source"] == "agent:config" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names) // deterministic warning order; map iteration isn't
	return names
}

func (s *incusSource) Open(ctx context.Context, instance string) (io.ReadCloser, func() error, error) {
	backupName := fmt.Sprintf("plakar-%d", time.Now().UnixNano())

	op, err := s.server.CreateInstanceBackup(instance, api.InstanceBackupsPost{
		Name:                 backupName,
		ExpiresAt:            time.Now().Add(s.backupTTL),
		InstanceOnly:         true,
		OptimizedStorage:     false,
		CompressionAlgorithm: "none",
	})
	if err != nil {
		return nil, nil, fmt.Errorf("incus: create backup: %w", err)
	}
	cleanup := func() error {
		delOp, err := s.server.DeleteInstanceBackup(instance, backupName)
		if err != nil {
			return err
		}
		// cleanup has no caller-supplied ctx (it runs from a deferred
		// closure after Open returns), so a frozen daemon must not be
		// allowed to hang it forever: bound the wait with our own timeout.
		waitCtx, cancel := context.WithTimeout(context.Background(), s.cleanupTimeout)
		defer cancel()
		return delOp.WaitContext(waitCtx)
	}

	if err := op.WaitContext(ctx); err != nil {
		// The POST was accepted, so the server-side backup object may
		// already exist even though we're erroring out (e.g. ctx was
		// cancelled while waiting). Best-effort delete it now instead
		// of relying solely on the ExpiresAt TTL; the contract is
		// "error return => nil cleanup", so any delete failure here is
		// deliberately swallowed.
		_ = cleanup()
		return nil, nil, wrapBackupError(instance, err)
	}

	// The InstanceServer interface exposed by github.com/lxc/incus/v6/client
	// has no raw DoHTTP escape hatch (verified via `go doc`), so the export
	// endpoint can't be streamed directly from an *http.Response body as
	// earlier drafted. Instead, GetInstanceBackupFile pumps the tarball into
	// an io.WriteSeeker synchronously; bridge that to the io.ReadCloser this
	// method must return via an io.Pipe, with the write side driven from a
	// goroutine.
	pr, pw := io.Pipe()
	sink := &pipeWriteSeeker{w: pw}

	// GetInstanceBackupFile blocks on a synchronous HTTP GET with no ctx
	// parameter of its own; wire ctx cancellation through the request's
	// Canceler so a caller-side cancel (or a stalled daemon connection)
	// actually interrupts the download instead of hanging the goroutine
	// below forever.
	canceller := cancel.NewHTTPRequestCanceller()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = canceller.Cancel()
		case <-done:
		}
	}()

	go func() {
		_, err := s.server.GetInstanceBackupFile(instance, backupName, &incus.BackupFileRequest{
			BackupFile: sink,
			Canceler:   canceller,
		})
		close(done)
		// CloseWithError(nil) is equivalent to a plain Close: the reader
		// observes a clean io.EOF once buffered data is drained.
		_ = pw.CloseWithError(err)
	}()

	return pr, cleanup, nil
}

// wrapBackupError turns a failed server-side backup creation into an
// actionable error when the cause is exhausted disk space. Creating
// the backup is where Incus materializes the complete tarball on the
// server's own disk before we can stream it (see Open), so on a tight
// host this is the operation that hits ENOSPC first — and the raw
// Incus error says neither where that space is needed nor how to get
// more of it. Matching on the error text is the only option: the API
// returns a flat string, not a typed errno.
func wrapBackupError(instance string, err error) error {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "no space left on device") || strings.Contains(msg, "disk quota exceeded") {
		return fmt.Errorf("incus: backup %s: %w (Incus stages the full backup tarball on the server before streaming it: free up space under the server's backups path — /var/lib/incus/backups by default — or point the server config key storage.backups_volume at a storage pool with enough room, then retry)", instance, err)
	}
	return fmt.Errorf("incus: backup %s: %w", instance, err)
}

// pipeWriteSeeker adapts an *io.PipeWriter to the io.WriteSeeker interface
// required by BackupFileRequest.BackupFile. The Incus client only ever
// queries the current write offset (Seek(0, io.SeekCurrent)) to report
// download progress; it never seeks backwards or forwards over a pipe,
// so any other seek is rejected.
type pipeWriteSeeker struct {
	w      *io.PipeWriter
	offset int64
}

func (p *pipeWriteSeeker) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.offset += int64(n)
	return n, err
}

func (p *pipeWriteSeeker) Seek(offset int64, whence int) (int64, error) {
	if whence == io.SeekCurrent && offset == 0 {
		return p.offset, nil
	}
	return 0, fmt.Errorf("incus: unsupported seek (offset=%d whence=%d)", offset, whence)
}
