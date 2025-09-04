package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-stdio/exporter"
)

func main() {
	sdk.EntrypointExporter(os.Args, exporter.NewStdioExporter)
}
