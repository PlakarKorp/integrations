package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	etcd "github.com/PlakarKorp/integrations/etcd"
)

func main() {
	server.EntrypointImporter(etcd.NewImporter, os.Args)
}
