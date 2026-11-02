package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	connector "github.com/PlakarKorp/integrations/example"
)

func main() {
	server.EntrypointImporter(connector.NewImporter, os.Args)
}
