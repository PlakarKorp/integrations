package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	fs "github.com/PlakarKorp/integration-fs/exporter"
)

func main() {
	sdk.EntrypointExporter(os.Args, fs.NewFSExporter)
}
