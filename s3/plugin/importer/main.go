package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/s3/importer"
)

func main() {
	server.EntrypointImporter(importer.NewS3Importer, os.Args)
}
