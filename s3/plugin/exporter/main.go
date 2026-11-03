package main

import (
	"os"

	sdk "github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/s3/exporter"
)

func main() {
	sdk.EntrypointExporter(exporter.NewS3Exporter, os.Args)
}
