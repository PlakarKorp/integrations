package gitlab

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	iimporter "github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

const importerFlags = location.FLAG_STREAM | location.FLAG_NEEDACK

type Importer struct {
	config Config
}

func NewImporter(_ context.Context, _ *connectors.Options, proto string, config map[string]string) (iimporter.Importer, error) {
	cfg, err := NewConfig(proto, config)
	if err != nil {
		return nil, err
	}
	return &Importer{config: cfg}, nil
}

func (i *Importer) Origin() string                 { return i.config.Origin() }
func (i *Importer) Type() string                   { return i.config.Proto }
func (i *Importer) Root() string                   { return "/" }
func (i *Importer) Flags() location.Flags          { return importerFlags }
func (i *Importer) Ping(ctx context.Context) error { return i.config.Ping(ctx) }
func (i *Importer) Close(_ context.Context) error  { return nil }
func (i *Importer) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	backupPath, err := i.config.CreateBackup(ctx)
	if err != nil {
		return fmt.Errorf("creating GitLab backup: %w", err)
	}
	if err := i.emitFile(ctx, records, results, backupPath, "/backup/"+path.Base(backupPath)); err != nil {
		return err
	}
	for _, configPath := range i.config.ConfigPaths {
		if err := i.emitOptionalFile(ctx, records, results, configPath, "/config/"+path.Base(configPath)); err != nil {
			return err
		}
	}
	return nil
}

func (i *Importer) emitOptionalFile(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result, sourcePath, snapshotPath string) error {
	if !i.config.PathExists(ctx, sourcePath) {
		return nil
	}
	return i.emitFile(ctx, records, results, sourcePath, snapshotPath)
}

func (i *Importer) emitFile(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result, sourcePath, snapshotPath string) error {
	now := time.Now().UTC()
	size := int64(0)
	if !i.config.remote() {
		if info, err := os.Stat(sourcePath); err == nil {
			size = info.Size()
			now = info.ModTime()
		}
	}

	fileinfo := objects.FileInfo{
		Lname:    filepath.Base(sourcePath),
		Lsize:    size,
		Lmode:    0o444,
		LmodTime: now,
	}
	record := connectors.NewRecord(snapshotPath, "", fileinfo, nil, func() (io.ReadCloser, error) {
		return i.config.OpenPath(ctx, sourcePath)
	})

	select {
	case <-ctx.Done():
		return ctx.Err()
	case records <- record:
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case result := <-results:
		if result != nil && result.Err != nil {
			return result.Err
		}
		return nil
	}
}

var _ iimporter.Importer = (*Importer)(nil)
