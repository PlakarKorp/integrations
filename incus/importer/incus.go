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
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
)

const defaultSocket = "/var/lib/incus/unix.socket"

// tar streaming is strictly sequential: each record must be acked
// before reading the next entry.
const flags = location.FLAG_STREAM | location.FLAG_NEEDACK

func init() {
	importer.Register("incus", flags, NewImporter)
}

// backupSource abstracts the Incus API so the import pipeline is
// testable without a running daemon.
type backupSource interface {
	Ping(ctx context.Context) error
	// Open creates a fresh portable backup of the instance and
	// returns its tar stream plus a cleanup func deleting the
	// server-side backup.
	Open(ctx context.Context, instance string) (io.ReadCloser, func() error, error)
}

type Importer struct {
	instance string
	src      backupSource
}

func NewImporter(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (importer.Importer, error) {
	instance := strings.TrimPrefix(config["location"], "incus://")
	if instance == "" || strings.Contains(instance, "/") {
		return nil, fmt.Errorf("incus: invalid instance name %q", instance)
	}
	socket := config["socket"]
	if socket == "" {
		socket = defaultSocket
	}
	src, err := newIncusSource(socket)
	if err != nil {
		return nil, err
	}
	return newImporterWithSource(instance, src), nil
}

func newImporterWithSource(instance string, src backupSource) *Importer {
	return &Importer{instance: instance, src: src}
}

func (p *Importer) Type() string          { return "incus" }
func (p *Importer) Root() string          { return "/" }
func (p *Importer) Flags() location.Flags { return flags }

func (p *Importer) Origin() string {
	hostname, err := os.Hostname()
	if err != nil {
		return "incus"
	}
	return hostname
}

func (p *Importer) Ping(ctx context.Context) error { return p.src.Ping(ctx) }

func (p *Importer) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	stream, cleanup, err := p.src.Open(ctx, p.instance)
	if err != nil {
		return err
	}
	defer func() {
		stream.Close()
		if cleanup != nil {
			_ = cleanup()
		}
	}()

	tr := tar.NewReader(stream)
	for {
		hdr, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		records <- &connectors.Record{
			Pathname: recordPath(hdr),
			Target:   hdr.Linkname,
			FileInfo: finfo(hdr),
			Reader:   io.NopCloser(tr),
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-results:
			// sequential stream: wait for the ack before advancing.
		}
	}
}

func (p *Importer) Close(ctx context.Context) error { return nil }

// newIncusSource is implemented in incus_api.go (real Incus client).
