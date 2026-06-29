package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integrations/caldav"
)

func main() {
	sdk.EntrypointExporter(os.Args, caldav.NewExporter)
}
