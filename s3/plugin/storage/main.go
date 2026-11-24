package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/s3/storage"
)

func main() {
	server.EntrypointStorage(storage.NewStore, os.Args)
}
