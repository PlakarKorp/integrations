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
	"fmt"
	"io"
	"os"
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
	hostname, err := os.Hostname()
	if err != nil {
		return "incus"
	}
	return hostname
}

func (p *Exporter) Ping(ctx context.Context) error { return p.sink.Ping(ctx) }

func (p *Exporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) (ret error) {
	defer close(results)

	pr, pw := io.Pipe()
	sinkDone := make(chan error, 1)
	go func() { sinkDone <- p.sink.Restore(ctx, p.instance, pr) }()

	tw := tar.NewWriter(pw)

	// The tar stream is sequential by nature: records are written in
	// the order they arrive, one at a time.
loop:
	for {
		select {
		case <-ctx.Done():
			ret = ctx.Err()
			break loop

		case rec, ok := <-records:
			if !ok {
				break loop
			}
			if rec.Err != nil || rec.IsXattr {
				results <- rec.Ok()
				continue
			}

			hdr, err := tarHeader(rec)
			if err != nil {
				results <- rec.Error(err)
				continue
			}
			if err := tw.WriteHeader(hdr); err != nil {
				results <- rec.Error(err)
				ret = err
				break loop
			}
			if hdr.Typeflag == tar.TypeReg && rec.Reader != nil {
				if _, err := io.Copy(tw, rec.Reader); err != nil {
					results <- rec.Error(err)
					ret = err
					break loop
				}
			}
			results <- rec.Ok()
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

	if err := <-sinkDone; err != nil && ret == nil {
		ret = fmt.Errorf("incus: restore %s: %w", p.instance, err)
	}
	return ret
}

func (p *Exporter) Close(ctx context.Context) error { return nil }
