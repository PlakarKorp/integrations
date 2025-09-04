package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	fs "github.com/PlakarKorp/integration-fs/importer"
)

func main() {
	sdk.EntrypointImporter(os.Args, fs.NewFSImporter)
}
