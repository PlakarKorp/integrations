package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-s3/exporter"
)

func main() {
	sdk.EntrypointExporter(os.Args, exporter.NewS3Exporter)
}
