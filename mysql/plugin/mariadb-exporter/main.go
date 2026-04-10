package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-mysql/exporter"
)

func main() {
	sdk.EntrypointExporter(os.Args, exporter.NewMariaDB)
}
