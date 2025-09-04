package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-sftp/exporter"
)

func main() {
	sdk.EntrypointExporter(os.Args, exporter.NewSFTPExporter)
}
