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
	"time"

	incus "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
)

type incusSource struct {
	server incus.InstanceServer
}

func newIncusSource(socket string) (backupSource, error) {
	server, err := incus.ConnectIncusUnix(socket, nil)
	if err != nil {
		return nil, fmt.Errorf("incus: connect %s: %w", socket, err)
	}
	return &incusSource{server: server}, nil
}

func (s *incusSource) Ping(ctx context.Context) error {
	_, _, err := s.server.GetServer()
	return err
}

func (s *incusSource) Open(ctx context.Context, instance string) (io.ReadCloser, func() error, error) {
	backupName := fmt.Sprintf("plakar-%d", time.Now().UnixNano())

	op, err := s.server.CreateInstanceBackup(instance, api.InstanceBackupsPost{
		Name:                 backupName,
		ExpiresAt:            time.Now().Add(6 * time.Hour),
		InstanceOnly:         true,
		OptimizedStorage:     false,
		CompressionAlgorithm: "none",
	})
	if err != nil {
		return nil, nil, fmt.Errorf("incus: create backup: %w", err)
	}
	if err := op.WaitContext(ctx); err != nil {
		return nil, nil, fmt.Errorf("incus: backup %s: %w", instance, err)
	}

	cleanup := func() error {
		delOp, err := s.server.DeleteInstanceBackup(instance, backupName)
		if err != nil {
			return err
		}
		return delOp.Wait()
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

	go func() {
		_, err := s.server.GetInstanceBackupFile(instance, backupName, &incus.BackupFileRequest{
			BackupFile: sink,
		})
		// CloseWithError(nil) is equivalent to a plain Close: the reader
		// observes a clean io.EOF once buffered data is drained.
		_ = pw.CloseWithError(err)
	}()

	return pr, cleanup, nil
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
