package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	forgejo "github.com/PlakarKorp/integration-forgejo"
)

func main() {
	sdk.EntrypointImporter(os.Args, forgejo.NewImporter)
}
