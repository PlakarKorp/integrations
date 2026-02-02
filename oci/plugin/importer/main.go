package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk/v2"
	"github.com/PlakarKorp/integration-oci/storage"
)

func main() {
	sdk.EntrypointImporter(os.Args, storage.New)
}
