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
	"sort"
	"strings"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

const defaultSocket = "/var/lib/incus/unix.socket"

// paxXattrPrefix is how GNU/BSD tar encode a POSIX extended attribute
// (e.g. security.capability) as a PAX extended header record: the key
// is this prefix plus the xattr name, the value is the raw xattr
// value. archive/tar exposes these via hdr.PAXRecords (hdr.Xattrs is
// the older, deprecated "SCHILY.xattr." accessor and is not populated
// for records read this way in modern archive/tar; PAXRecords is the
// one to use).
const paxXattrPrefix = "SCHILY.xattr."

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
	// The snapshot origin is the instance itself, not the node the
	// plugin happens to run on: it names snapshots in listings/UI.
	return p.instance
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

		select {
		case records <- &connectors.Record{
			Pathname: recordPath(hdr),
			Target:   linkTarget(hdr),
			FileInfo: finfo(hdr),
			Reader:   io.NopCloser(tr),
		}:
		case <-ctx.Done():
			return ctx.Err()
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-results:
			// sequential stream: wait for the ack before advancing.
		}

		if err := p.emitXattrs(ctx, hdr, records, results); err != nil {
			return err
		}
	}
}

// emitXattrs replays a tar entry's extended attributes as one kloset
// xattr Record per attribute, emitted right after the owning file
// record (same Pathname) and acked the same sequential way - mirroring
// the reference "fs" importer (plakar-integrations/fs/importer/walkdir.go),
// which emits its file record first and then one connectors.NewXattr
// per attribute. The incus exporter's tar rebuild relies on kloset
// replaying records in import order, so this ordering is load-bearing:
// an xattr record must arrive immediately after the file record it
// belongs to, not before and not batched separately.
func (p *Importer) emitXattrs(ctx context.Context, hdr *tar.Header, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	if len(hdr.PAXRecords) == 0 {
		return nil
	}

	names := make([]string, 0, len(hdr.PAXRecords))
	for k := range hdr.PAXRecords {
		if name, ok := strings.CutPrefix(k, paxXattrPrefix); ok && name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names) // deterministic emission order; map iteration isn't

	path := recordPath(hdr)
	for _, name := range names {
		value := hdr.PAXRecords[paxXattrPrefix+name]
		select {
		case records <- &connectors.Record{
			Pathname:  path,
			IsXattr:   true,
			XattrName: name,
			XattrType: objects.AttributeExtended,
			Reader:    io.NopCloser(strings.NewReader(value)),
		}:
		case <-ctx.Done():
			return ctx.Err()
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-results:
			// sequential stream: wait for the ack before advancing.
		}
	}
	return nil
}

func (p *Importer) Close(ctx context.Context) error { return nil }

// newIncusSource is implemented in incus_api.go (real Incus client).
