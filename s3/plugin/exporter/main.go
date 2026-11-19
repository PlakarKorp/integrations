package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/s3/exporter"
)

func main() {
	server.EntrypointExporter(exporter.NewS3Exporter, os.Args)
}
