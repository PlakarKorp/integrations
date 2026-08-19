package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integrations/routeros"
)

func main() {
	sdk.EntrypointImporter(os.Args, routeros.NewImporter)
}
