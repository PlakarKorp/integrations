package gitlab

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/PlakarKorp/kloset/connectors"
	eexporter "github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
)

type Exporter struct {
	config   Config
	backupID string
}

func NewExporter(_ context.Context, _ *connectors.Options, proto string, config map[string]string) (eexporter.Exporter, error) {
	cfg, err := NewConfig(proto, config)
	if err != nil {
		return nil, err
	}
	return &Exporter{config: cfg}, nil
}

func (e *Exporter) Origin() string                 { return e.config.Origin() }
func (e *Exporter) Type() string                   { return e.config.Proto }
func (e *Exporter) Root() string                   { return "/" }
func (e *Exporter) Flags() location.Flags          { return 0 }
func (e *Exporter) Ping(ctx context.Context) error { return e.config.Ping(ctx) }
func (e *Exporter) Close(_ context.Context) error  { return nil }

func (e *Exporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case record, ok := <-records:
			if !ok {
				if e.backupID == "" {
					return fmt.Errorf("no GitLab backup archive found in snapshot")
				}
				return e.config.Restore(ctx, e.backupID)
			}
			results <- e.restoreRecord(ctx, record)
		}
	}
}

func (e *Exporter) restoreRecord(ctx context.Context, record *connectors.Record) *connectors.Result {
	if record.FileInfo.Lmode.IsDir() || record.Pathname == "manifest.json" {
		return record.Ok()
	}
	if record.Reader == nil {
		return record.Error(fmt.Errorf("record %s has no reader", record.Pathname))
	}

	base := path.Base(record.Pathname)
	switch {
	case strings.HasSuffix(base, "_gitlab_backup.tar"):
		dst := filepath.Join(e.config.BackupPath, base)
		if err := e.config.WritePath(ctx, dst, record.Reader, 0o600); err != nil {
			return record.Error(fmt.Errorf("writing backup archive %s: %w", dst, err))
		}
		e.backupID = backupIDFromArchiveName(base)
		return record.Ok()

	case strings.HasPrefix(strings.TrimPrefix(record.Pathname, "/"), "config/"):
		dst := filepath.Join(e.config.ConfigDir, base)
		if err := e.config.WritePath(ctx, dst, record.Reader, 0o600); err != nil {
			return record.Error(fmt.Errorf("writing GitLab config %s: %w", dst, err))
		}
		return record.Ok()

	default:
		return record.Error(fmt.Errorf("unexpected GitLab snapshot file: %s", record.Pathname))
	}
}

func backupIDFromArchiveName(name string) string {
	return strings.TrimSuffix(path.Base(name), "_gitlab_backup.tar")
}

var _ eexporter.Exporter = (*Exporter)(nil)
