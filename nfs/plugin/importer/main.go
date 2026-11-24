package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/nfs/importer"
)

func main() {
	server.EntrypointImporter(importer.NewImporter, os.Args)
}
