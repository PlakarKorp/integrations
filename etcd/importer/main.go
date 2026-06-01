package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	etcd "github.com/PlakarKorp/integrations/etcd"
)

func main() {
	sdk.EntrypointImporter(os.Args, etcd.NewImporter)
}
