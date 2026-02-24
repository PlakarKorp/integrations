package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	connector "github.com/tracepanic/plakar-integration"
)

func main() {
	sdk.EntrypointExporter(os.Args, connector.NewExporter)
}
