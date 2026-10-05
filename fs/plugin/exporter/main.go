package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	fs "github.com/PlakarKorp/integrations/fs/exporter"
)

func main() {
	sdk.EntrypointExporter(os.Args, fs.NewFSExporter)
}
