package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/rclone"
)

func main() {
	server.EntrypointStorage(rclone.NewStorage, os.Args)
}
