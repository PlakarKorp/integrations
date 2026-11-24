package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	azblob "github.com/PlakarKorp/integrations/azblob"
)

func main() {
	server.EntrypointExporter(azblob.NewExporter, os.Args)
}
