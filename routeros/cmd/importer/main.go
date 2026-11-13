//nolint:misspell // RouterOS is a product name.
package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/routeros"
)

func main() {
	server.EntrypointImporter(routeros.NewImporter, os.Args)
}
