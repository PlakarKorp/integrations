package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/oci/storage"
)

func main() {
	server.EntrypointStorage(storage.New, os.Args)
}
