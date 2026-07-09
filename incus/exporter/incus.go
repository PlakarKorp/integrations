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

package exporter

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
)

const defaultSocket = "/var/lib/incus/unix.socket"

func init() {
	exporter.Register("incus", 0, NewExporter)
}

// restoreSink abstracts the Incus restore API for testability.
type restoreSink interface {
	Ping(ctx context.Context) error
	// Restore consumes a portable backup tar stream and creates the
	// named instance from it.
	Restore(ctx context.Context, instance string, tarStream io.Reader) error
}

type Exporter struct {
	instance string
	sink     restoreSink
}

func NewExporter(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (exporter.Exporter, error) {
	instance := strings.TrimPrefix(config["location"], "incus://")
	if instance == "" || strings.Contains(instance, "/") {
		return nil, fmt.Errorf("incus: invalid instance name %q", instance)
	}
	socket := config["socket"]
	if socket == "" {
		socket = defaultSocket
	}
	sink, err := newIncusSink(socket, config["pool"])
	if err != nil {
		return nil, err
	}
	return newExporterWithSink(instance, sink), nil
}

func newExporterWithSink(instance string, sink restoreSink) *Exporter {
	return &Exporter{instance: instance, sink: sink}
}

func (p *Exporter) Type() string          { return "incus" }
func (p *Exporter) Root() string          { return "/" }
func (p *Exporter) Flags() location.Flags { return 0 }

func (p *Exporter) Origin() string {
	return p.instance
}

func (p *Exporter) Ping(ctx context.Context) error { return p.sink.Ping(ctx) }

func (p *Exporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) (ret error) {
	defer close(results)

	pr, pw := io.Pipe()
	sinkDone := make(chan error, 1)
	go func() {
		err := p.sink.Restore(ctx, p.instance, pr)
		// Always close the read end, even when Restore returns early
		// without draining the stream: otherwise the tar writer below
		// stays blocked forever on pw.Write, deadlocking Export when
		// the sink rejects the upload instead of consuming it fully.
		pr.CloseWithError(err)
		sinkDone <- err
	}()

	tw := tar.NewWriter(pw)

	// The tar stream is sequential by nature: records are written in
	// the order they arrive, one at a time.
	//
	// The rebuilt tar preserves record ARRIVAL ORDER, and Incus expects
	// backup/index.yaml first. Correctness here relies on kloset replaying
	// records in import order (true today, beta-validated); if kloset ever
	// reorders records, this needs revisiting.
	//
	// xattr records (rec.IsXattr) arrive right after the file record they
	// belong to (same Pathname) - that's the ordering the incus importer
	// emits (mirroring plakar-integrations/fs/importer) and the one this
	// exporter's "replay in import order" guarantee above depends on. So a
	// file record can't be written immediately: it must be held ("pending")
	// until either the next non-xattr record or end-of-stream tells us no
	// more xattrs are coming for it, accumulating any xattrs seen in the
	// meantime into its tar header's PAXRecords. Holding the record is
	// cheap and safe: its Reader is repo-backed/lazy, not a live resource
	// that needs immediate draining.
	var (
		pending       *connectors.Record
		pendingXattrs map[string]string
	)

	// flushPending writes the held record (if any) as a tar entry, with
	// any accumulated xattrs folded into its PAXRecords, and acks it.
	// It returns a non-nil error only for failures that corrupt the tar
	// stream itself (WriteHeader/body copy) - those abort Export. Header
	// build errors and the nil-reader guard are per-record and reported
	// via an Error result without aborting the rest of the stream, exactly
	// as before this change.
	flushPending := func() error {
		if pending == nil {
			return nil
		}
		p, xattrs := pending, pendingXattrs
		pending, pendingXattrs = nil, nil

		hdr, err := tarHeader(p)
		if err != nil {
			results <- p.Error(err)
			return nil
		}
		if len(xattrs) > 0 {
			hdr.PAXRecords = xattrs
		}
		if hdr.Typeflag == tar.TypeReg && hdr.Size > 0 && p.Reader == nil {
			// Writing this header would promise hdr.Size bytes of
			// body that we have nothing to supply, corrupting the
			// tar stream for every entry that follows. Reject the
			// record instead of emitting a header with no body.
			results <- p.Error(fmt.Errorf("incus: record %q has size %d but no reader", p.Pathname, hdr.Size))
			return nil
		}
		if err := tw.WriteHeader(hdr); err != nil {
			results <- p.Error(err)
			return err
		}
		if hdr.Typeflag == tar.TypeReg && p.Reader != nil {
			n, err := io.Copy(tw, p.Reader)
			if err != nil {
				results <- p.Error(err)
				return err
			}
			if n != hdr.Size {
				// The tar stream is already corrupted at this point (the
				// header promised hdr.Size bytes of body): a short read
				// here would otherwise surface as a cryptic error on the
				// *next* WriteHeader call. Fail this record now with a
				// clear message instead.
				err := fmt.Errorf("incus: %s: short read: got %d bytes, want %d", p.Pathname, n, hdr.Size)
				results <- p.Error(err)
				return err
			}
		}
		results <- p.Ok()
		return nil
	}

loop:
	for {
		select {
		case <-ctx.Done():
			ret = ctx.Err()
			if pending != nil {
				results <- pending.Error(ret)
				pending, pendingXattrs = nil, nil
			}
			break loop

		case rec, ok := <-records:
			if !ok {
				break loop
			}
			if rec.Err != nil {
				if err := flushPending(); err != nil {
					results <- rec.Error(err)
					ret = err
					break loop
				}
				results <- rec.Ok()
				continue
			}
			if rec.IsXattr {
				if pending == nil || pending.Pathname != rec.Pathname {
					// Protocol violation: an xattr record must follow
					// its owning file record with the same Pathname.
					results <- rec.Error(fmt.Errorf("incus: xattr record %q (%s) has no matching pending file record", rec.Pathname, rec.XattrName))
					continue
				}
				value, err := io.ReadAll(rec.Reader)
				if err != nil {
					results <- rec.Error(err)
					continue
				}
				if pendingXattrs == nil {
					pendingXattrs = make(map[string]string)
				}
				pendingXattrs["SCHILY.xattr."+rec.XattrName] = string(value)
				results <- rec.Ok()
				continue
			}

			// A new file/dir/symlink record: whatever was pending has
			// now seen all of its xattrs (if any), flush it.
			if err := flushPending(); err != nil {
				results <- rec.Error(err)
				ret = err
				break loop
			}
			pending = rec
		}
	}

	if pending != nil {
		if err := flushPending(); err != nil && ret == nil {
			ret = err
		}
	}

	if err := tw.Close(); err != nil && ret == nil {
		ret = err
	}
	if ret != nil {
		pw.CloseWithError(ret)
	} else {
		pw.Close()
	}

	// The sink's error is the actionable one: it explains *why* the pipe
	// broke. When the write loop only observed the pipe closing (nil, or
	// the unhelpful io.ErrClosedPipe), surface the sink's error instead of
	// masking it.
	if sinkErr := <-sinkDone; sinkErr != nil && (ret == nil || errors.Is(ret, io.ErrClosedPipe)) {
		ret = fmt.Errorf("incus: restore %s: %w", p.instance, sinkErr)
	}
	return ret
}

func (p *Exporter) Close(ctx context.Context) error { return nil }
