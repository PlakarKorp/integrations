package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/postgresql/binimporter"
)

func main() {
	server.EntrypointImporter(binimporter.NewBinImporter, os.Args)
}
