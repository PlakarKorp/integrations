package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	connector "github.com/PlakarKorp/integration-example"
)

func main() {
	sdk.EntrypointImporter(os.Args, connector.NewImporter)
}
