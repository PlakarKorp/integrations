package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/sqlite/storage"
)

func main() {
	server.EntrypointStorage(storage.NewStore, os.Args)
}
