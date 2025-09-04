package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-sftp/importer"
)

func main() {
	sdk.EntrypointImporter(os.Args, importer.NewSFTPImporter)
}
