package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	gitlab "github.com/PlakarKorp/integration-gitlab"
)

func main() {
	sdk.EntrypointImporter(os.Args, gitlab.NewImporter)
}
