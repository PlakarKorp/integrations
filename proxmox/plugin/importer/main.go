package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/gillesdubois/plakar-integration-proxmox/importer"
)

func main() {
	sdk.EntrypointImporter(os.Args, importer.NewProxmoxImporter)
}
