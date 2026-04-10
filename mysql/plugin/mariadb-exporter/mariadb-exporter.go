package main

import (
	"context"
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-mysql/exporter"
	"github.com/PlakarKorp/integration-mysql/mysqlconn"
	"github.com/PlakarKorp/kloset/connectors"
	iexporter "github.com/PlakarKorp/kloset/connectors/exporter"
)

func newMariaDB(_ context.Context, _ *connectors.Options, proto string, config map[string]string) (iexporter.Exporter, error) {
	conn, err := mysqlconn.ParseConnConfig(config)
	if err != nil {
		return nil, err
	}
	conn.ClientBin = "mariadb"
	conn.DumpBin = "mariadb-dump"
	return exporter.New(proto, conn, config)
}

func main() {
	sdk.EntrypointExporter(os.Args, newMariaDB)
}
