package importer

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	fsimporter "github.com/PlakarKorp/integrations/fs/importer"
	pgimporter "github.com/PlakarKorp/integrations/postgresql/importer"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
)

func init() {
	importer.Register("pcp", location.FLAG_STREAM, NewImporter)
}

type SubImporter struct {
	Prefix   string
	Importer importer.Importer
}

type Importer struct {
	subImporters []SubImporter
}

const APPLIANCE_DATA_DIR = "/appliance_data"

func NewImporter(appCtx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	subImporters := []SubImporter{}

	rawPassword, err := os.ReadFile(filepath.Join(APPLIANCE_DATA_DIR, "secrets/db_password"))
	if err != nil {
		return nil, fmt.Errorf("reading db password: %w", err)
	}

	pgImporter, err := pgimporter.NewImporter(appCtx, opts, "postgres", map[string]string{
		"host":     "postgres",
		"port":     "5432",
		"username": "plakman",
		"password": strings.TrimSpace(string(rawPassword)),
	})
	if err != nil {
		return nil, err
	}

	subImporters = append(subImporters, SubImporter{
		Prefix:   "/db",
		Importer: pgImporter,
	})

	for _, name := range []string{"secrets", "ssh", "logs", "plakman", "plakman_runtime/instance.key", "plakman_runtime/license.jwt", "plakman_runtime/pki"} {
		path := filepath.Join(APPLIANCE_DATA_DIR, name)

		fsImporter, err := fsimporter.NewFSImporter(appCtx, opts, "fs", map[string]string{
			"location": "fs://" + path,
		})
		if err != nil {
			return nil, err
		}

		subImporters = append(subImporters, SubImporter{
			Prefix:   "/fs",
			Importer: fsImporter,
		})
	}

	return &Importer{
		subImporters: subImporters,
	}, nil
}

func (p *Importer) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	// Each sub-importer closes its own records channel, so we give each one a
	// private subRecords channel and relay into the shared records channel ourselves.
	// results is passed through unchanged: none of our sub-importers are
	// stream-based, so they never read acks from it.
	for _, sub := range p.subImporters {
		subRecords := make(chan *connectors.Record)
		err := make(chan error, 1)

		go func(sub importer.Importer) {
			err <- sub.Import(ctx, subRecords, results)
		}(sub.Importer)

		for rec := range subRecords {
			rec.Pathname = path.Join(sub.Prefix, rec.Pathname)
			records <- rec
		}

		if subErr := <-err; subErr != nil {
			return subErr
		}
	}
	return nil
}

func (p *Importer) Ping(ctx context.Context) error {
	for _, sub := range p.subImporters {
		if err := sub.Importer.Ping(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (p *Importer) Close(ctx context.Context) error {
	for _, sub := range p.subImporters {
		if err := sub.Importer.Close(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (p *Importer) Root() string   { return "/" }
func (p *Importer) Origin() string { return "pcp" }
func (p *Importer) Type() string   { return "pcp" }

// FLAG_STREAM tells the framework not to pre-count records. Without it, the
// framework runs the importer twice to populate the progress bar — which would
// trigger two full PostgreSQL backups.
func (p *Importer) Flags() location.Flags {
	return location.FLAG_STREAM
}
