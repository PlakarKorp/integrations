package main

import (
	"os"

	connector "github.com/PlakarKorp/integrations/openwrt"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
)

func main() {
	sdk.EntrypointExporter(os.Args, connector.NewExporter)
}
