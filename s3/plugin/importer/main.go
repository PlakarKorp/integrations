package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-s3/importer"
)

func main() {
	sdk.EntrypointImporter(os.Args, importer.NewS3Importer)
}
