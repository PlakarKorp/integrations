package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	azblob "github.com/PlakarKorp/integration-azblob"
)

func main() {
	sdk.EntrypointImporter(os.Args, azblob.NewImporter)
}
