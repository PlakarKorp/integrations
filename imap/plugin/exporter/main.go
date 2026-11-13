package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/imap/exporter"
)

func main() {
	server.EntrypointExporter(exporter.NewExporter, os.Args)
}
