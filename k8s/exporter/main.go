package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	k8s "github.com/PlakarKorp/integration-k8s"
)

func main() {
	sdk.EntrypointExporter(os.Args, k8s.NewExporter)
}
