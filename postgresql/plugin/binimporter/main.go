package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-postgresql/binimporter"
)

func main() {
	sdk.EntrypointImporter(os.Args, binimporter.NewBinImporter)
}
