package main

import (
	"context"
	"os"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/mysql/exporter"
	"github.com/PlakarKorp/integrations/mysql/mysqlconn"
)

func newMariaDB(_ context.Context, _ *integration.Options, proto string, config map[string]string) (integration.Exporter, error) {
	conn, err := mysqlconn.ParseConnConfig(false, config)
	if err != nil {
		return nil, err
	}
	conn.ClientBin = "mariadb"
	conn.DumpBin = "mariadb-dump"
	conn.ExpectedFlavor = "mariadb"
	return exporter.New(proto, conn, config, "mariadb")
}

func main() {
	server.EntrypointExporter(newMariaDB, os.Args)
}
