package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integrations/docker"
)

func main() {
	sdk.EntrypointExporter(os.Args, docker.NewExporter)
}
