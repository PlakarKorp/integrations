package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	forgejo "github.com/PlakarKorp/integration-forgejo"
)

func main() {
	sdk.EntrypointExporter(os.Args, forgejo.NewExporter)
}
