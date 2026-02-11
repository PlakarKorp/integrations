package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/gillesdubois/plakar-integration-proxmox/exporter"
)

func main() {
	sdk.EntrypointExporter(os.Args, exporter.NewProxmoxExporter)
}
