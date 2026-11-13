package main

import (
	"os"

	connector "github.com/PlakarKorp/integrations/openwrt"

	"github.com/PlakarKorp/integration-sdk/server"
)

func main() {
	server.EntrypointImporter(connector.NewImporter, os.Args)
}
