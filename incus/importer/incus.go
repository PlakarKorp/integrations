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
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

// Defaults for the configurable timeouts. The backup TTL is a safety
// net: should the plugin die without running its cleanup, the server
// expires the temporary backup on its own. The cleanup timeout bounds
// the wait on the server-side backup deletion (which has no caller
// context). A big instance on a slow server may need larger values.
const (
	defaultBackupTTL      = 6 * time.Hour
	defaultCleanupTimeout = 2 * time.Minute
)

// durationOption reads a Go duration (e.g. "45m", "12h") from the
// config map, falling back to def when the key is absent. Zero and
// negative durations are rejected: they would make the backup expire
// immediately or disable the cleanup bound.
func durationOption(config map[string]string, key string, def time.Duration) (time.Duration, error) {
	raw, ok := config[key]
	if !ok {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("incus: invalid %s %q: %w", key, raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("incus: invalid %s %q: must be positive", key, raw)
	}
	return d, nil
}

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
	_ = importer.Register("incus", flags, NewImporter)
}

// backupSource abstracts the Incus API so the import pipeline is
// testable without a running daemon.
type backupSource interface {
	Ping(ctx context.Context) error
	// Open creates a fresh portable backup of the instance and
	// returns its tar stream plus a cleanup func deleting the
	// server-side backup.
	Open(ctx context.Context, instance string) (io.ReadCloser, func() error, error)
	// Inspect returns the instance's type and its disk devices other
	// than the root disk (custom volumes, host mounts): content the
	// backup does NOT include (InstanceOnly:true).
	Inspect(ctx context.Context, instance string) (instanceInfo, error)
}

// instanceInfo is what Inspect reports from a single GetInstance call:
// the instance type gates VM refusal, the extra disks feed the
// exclusion warnings.
type instanceInfo struct {
	Type       string
	ExtraDisks []string
}

type Importer struct {
	instance string
	project  string
	src      backupSource
	stderr   io.Writer
}

func NewImporter(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (importer.Importer, error) {
	instance := strings.TrimPrefix(config["location"], "incus://")
	if instance == "" || strings.Contains(instance, "/") {
		return nil, fmt.Errorf("incus: invalid instance name %q", instance)
	}
	backupTTL, err := durationOption(config, "backup_ttl", defaultBackupTTL)
	if err != nil {
		return nil, err
	}
	cleanupTimeout, err := durationOption(config, "cleanup_timeout", defaultCleanupTimeout)
	if err != nil {
		return nil, err
	}
	src, err := newIncusSource(config, backupTTL, cleanupTimeout)
	if err != nil {
		return nil, err
	}
	imp := newImporterWithSource(instance, src)
	imp.project = config["project"]
	if opts.Stderr != nil {
		// Stderr is msgpack:"-": nil when the plugin runs out of
		// process and Options crossed the wire.
		imp.stderr = opts.Stderr
	}
	return imp, nil
}

func newImporterWithSource(instance string, src backupSource) *Importer {
	return &Importer{instance: instance, src: src, stderr: io.Discard}
}

func (p *Importer) Type() string          { return "incus" }
func (p *Importer) Root() string          { return "/" }
func (p *Importer) Flags() location.Flags { return flags }

func (p *Importer) Origin() string {
	// The snapshot origin is the instance itself, not the node the
	// plugin happens to run on: it names snapshots in listings/UI.
	// Instance names are only unique within a project, so a non-default
	// project qualifies the origin ("project/instance") to keep
	// same-named instances from different projects distinguishable.
	// Without the project option the origin is unchanged, so snapshots
	// taken before this option existed keep their naming.
	if p.project != "" {
		return p.project + "/" + p.instance
	}
	return p.instance
}

func (p *Importer) Ping(ctx context.Context) error { return p.src.Ping(ctx) }

func (p *Importer) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	if err := p.preflight(ctx); err != nil {
		return err
	}

	stream, cleanup, err := p.src.Open(ctx, p.instance)
	if err != nil {
		return err
	}
	defer func() {
		_ = stream.Close()
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

// preflight inspects the instance before any server-side backup is
// created: it refuses virtual machines (a VM export is one monolithic
// disk image inside the tar — per-file browsing and dedup are
// meaningless on it and the path is untested, TODO-PROD #5) and warns,
// on stderr, about attached disk devices (custom volumes, host mounts)
// the backup will NOT contain (InstanceOnly:true covers the root
// filesystem only). An empty type means a legacy server predating the
// Type field and is treated as a container. The inspection itself is
// best-effort: a failure must not fail the backup (any real
// connectivity problem resurfaces in Open), so it is reported as a
// note rather than returned.
func (p *Importer) preflight(ctx context.Context) error {
	info, err := p.src.Inspect(ctx, p.instance)
	if err != nil {
		_, _ = fmt.Fprintf(p.stderr, "incus: note: could not inspect instance %s: %v\n", p.instance, err)
		return nil
	}
	if info.Type != "" && info.Type != "container" {
		return fmt.Errorf("incus: instance %s has type %q: only containers are supported (a %s backup would be a single monolithic disk image, with no per-file browsing or useful deduplication); use incus export for it instead", p.instance, info.Type, info.Type)
	}
	for _, name := range info.ExtraDisks {
		_, _ = fmt.Fprintf(p.stderr, "incus: WARNING: disk device %q of instance %s is attached but its content is NOT included in this backup (instance root filesystem only)\n", name, p.instance)
	}
	return nil
}

func (p *Importer) Close(ctx context.Context) error { return nil }

// newIncusSource is implemented in incus_api.go (real Incus client).
