package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-forgejo/importer"
)

func main() {
	sdk.EntrypointImporter(os.Args, importer.NewImporter)
}
