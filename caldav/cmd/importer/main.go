package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integrations/caldav"
)

func main() {
	sdk.EntrypointImporter(os.Args, caldav.NewImporter)
}
