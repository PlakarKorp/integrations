package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integrations/sftp/importer"
)

func main() {
	sdk.EntrypointImporter(os.Args, importer.NewImporter)
}
