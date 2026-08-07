package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integrations/routeros"
)

func main() {
	sdk.EntrypointExporter(os.Args, routeros.NewExporter)
}
