package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-mysql/importer"
)

func main() {
	sdk.EntrypointImporter(os.Args, importer.New)
}
