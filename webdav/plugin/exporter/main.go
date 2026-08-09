package main

import (
	"os"

	"github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integrations/webdav"
)

func main() {
	sdk.EntrypointExporter(os.Args, webdav.NewExporter)
}
