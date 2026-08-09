package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	gcs "github.com/PlakarKorp/integration-gcs"
)

func main() {
	sdk.EntrypointImporter(os.Args, gcs.NewImporter)
}
