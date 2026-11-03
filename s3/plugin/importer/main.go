package main

import (
	"os"

	sdk "github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/s3/importer"
)

func main() {
	sdk.EntrypointImporter(importer.NewS3Importer, os.Args)
}
