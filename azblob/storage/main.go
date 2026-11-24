package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	azblob "github.com/PlakarKorp/integrations/azblob"
)

func main() {
	server.EntrypointStorage(azblob.NewStore, os.Args)
}
