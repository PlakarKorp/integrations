package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/docker/importer"
)

func main() {
	server.EntrypointImporter(importer.NewImporter, os.Args)
}
