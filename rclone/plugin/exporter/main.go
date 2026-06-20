package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integrations/rclone"
)

func main() {
	sdk.EntrypointExporter(os.Args, rclone.NewExporter)
}
