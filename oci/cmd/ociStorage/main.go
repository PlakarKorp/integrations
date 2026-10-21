package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integrations/oci/storage"
)

func main() {
	sdk.EntrypointStorage(os.Args, storage.New)
}
