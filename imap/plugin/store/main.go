package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/imap/store"
)

func main() {
	server.EntrypointStorage(store.NewStore, os.Args)
}
