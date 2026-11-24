package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/rclone"
)

func main() {
	server.EntrypointExporter(rclone.NewExporter, os.Args)
}
