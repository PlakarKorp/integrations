package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integrations/rclone/importer"
)

func main() {
	sdk.EntrypointImporter(os.Args, importer.NewRcloneImporter)
}
