package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integrations/sftp/storage"
)

func main() {
	sdk.EntrypointStorage(os.Args, storage.NewStore)
}
