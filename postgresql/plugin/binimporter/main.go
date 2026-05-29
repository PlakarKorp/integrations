package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integrations/postgresql/binimporter"
)

func main() {
	sdk.EntrypointImporter(os.Args, binimporter.NewBinImporter)
}
