package main

import (
	"os"

	sdk "github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/s3/storage"
)

func main() {
	sdk.EntrypointStorage(storage.NewStore, os.Args)
}
