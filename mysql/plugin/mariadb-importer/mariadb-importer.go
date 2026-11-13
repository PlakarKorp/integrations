package main

import (
	"context"
	"os"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/mysql/importer"
	"github.com/PlakarKorp/integrations/mysql/manifest"
	"github.com/PlakarKorp/integrations/mysql/mysqlconn"
)

type mariadbImporter struct {
	importer.Importer
}

func newMariaDB(_ context.Context, _ *integration.Options, proto string, config map[string]string) (integration.Importer, error) {
	conn, err := mysqlconn.ParseConnConfig(false, config)
	if err != nil {
		return nil, err
	}
	conn.ClientBin = "mariadb"
	conn.DumpBin = "mariadb-dump"
	conn.ExpectedFlavor = "mariadb"

	base, err := importer.New(proto, conn, config)
	if err != nil {
		return nil, err
	}

	return &mariadbImporter{Importer: *base}, nil
}

func (m *mariadbImporter) Import(ctx context.Context, records chan<- *integration.Record, _ <-chan *integration.Result) (map[string]string, error) {
	if err := m.Conn.CheckFlavor(ctx, "mariadb"); err != nil {
		return nil, err
	}
	cfg := manifest.Config{
		Conn:     m.Conn,
		Flavor:   "mariadb",
		Database: m.Database,
		Options:  m.CommonManifestOptions(),
	}
	return nil, m.Run(ctx, records, cfg, nil)
}

func main() {
	server.EntrypointImporter(newMariaDB, os.Args)
}
