package main

import (
	"context"
	"os"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/mysql/exporter"
	"github.com/PlakarKorp/integrations/mysql/mysqlconn"
)

func newMySQL(_ context.Context, _ *integration.Options, proto string, config map[string]string) (integration.Exporter, error) {
	conn, err := mysqlconn.ParseConnConfig(false, config)
	if err != nil {
		return nil, err
	}
	conn.ClientBin = "mysql"
	conn.DumpBin = "mysqldump"
	conn.ExpectedFlavor = "mysql"
	return exporter.New(proto, conn, config, "mysql")
}

func main() {
	server.EntrypointExporter(newMySQL, os.Args)
}
