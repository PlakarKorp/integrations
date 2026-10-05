package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	fs "github.com/PlakarKorp/integrations/fs/importer"
)

func main() {
	sdk.EntrypointImporter(os.Args, fs.NewFSImporter)
}
