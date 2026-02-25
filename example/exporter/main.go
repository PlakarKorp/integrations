package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	connector "github.com/PlakarKorp/integration-example"
)

func main() {
	sdk.EntrypointExporter(os.Args, connector.NewExporter)
}
