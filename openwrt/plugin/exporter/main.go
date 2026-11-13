package main

import (
	"os"

	connector "github.com/PlakarKorp/integrations/openwrt"

	"github.com/PlakarKorp/integration-sdk/server"
)

func main() {
	server.EntrypointExporter(connector.NewExporter, os.Args)
}
