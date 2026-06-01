package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	connector "github.com/PlakarKorp/integrations/example"
)

func main() {
	sdk.EntrypointExporter(os.Args, connector.NewExporter)
}
