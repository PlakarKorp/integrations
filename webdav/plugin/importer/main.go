package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/webdav"
)

func main() {
	server.EntrypointImporter(webdav.NewImporter, os.Args)
}
