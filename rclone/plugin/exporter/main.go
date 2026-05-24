package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-rclone/exporter"
)

func main() {
	sdk.EntrypointExporter(os.Args, exporter.NewRcloneExporter)
}
