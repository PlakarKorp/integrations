package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	k8s "github.com/PlakarKorp/integrations/k8s"
)

func main() {
	sdk.EntrypointImporter(os.Args, k8s.NewImporter)
}
