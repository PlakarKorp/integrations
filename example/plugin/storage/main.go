package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	connector "github.com/PlakarKorp/integrations/example"
)

func main() {
	server.EntrypointStorage(connector.NewStore, os.Args)
}
