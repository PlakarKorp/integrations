package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integration-xcp-ng/xcp-ng/inventory"
)

func main() {
	server.EntrypointInventory(inventory.NewInventory, os.Args)
}
