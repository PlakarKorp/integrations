package main

import (
	"context"
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-mysql/importer"
	"github.com/PlakarKorp/integration-mysql/manifest"
	"github.com/PlakarKorp/integration-mysql/mysqlconn"
	"github.com/PlakarKorp/kloset/connectors"
	iimporter "github.com/PlakarKorp/kloset/connectors/importer"
)

type mariadbImporter struct {
	importer.Importer
}

func newMariaDB(_ context.Context, _ *connectors.Options, proto string, config map[string]string) (iimporter.Importer, error) {
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

func (m *mariadbImporter) Import(ctx context.Context, records chan<- *connectors.Record, _ <-chan *connectors.Result) error {
	if err := m.Conn.CheckFlavor(ctx, "mariadb"); err != nil {
		return err
	}
	cfg := manifest.Config{
		Conn:     m.Conn,
		Flavor:   "mariadb",
		Database: m.Database,
		Options:  m.CommonManifestOptions(),
	}
	return m.Run(ctx, records, cfg, nil)
}

func main() {
	sdk.EntrypointImporter(os.Args, newMariaDB)
}
