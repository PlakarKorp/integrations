package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/rclone"
)

func main() {
	server.EntrypointImporter(rclone.NewImporter, os.Args)
}
