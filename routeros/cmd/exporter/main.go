//nolint:misspell // RouterOS is a product name.
package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/routeros"
)

func main() {
	server.EntrypointExporter(routeros.NewExporter, os.Args)
}
