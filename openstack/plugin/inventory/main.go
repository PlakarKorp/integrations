package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/openstack/inventory"
)

func main() {
	server.EntrypointInventory(inventory.NewInventory, os.Args)
}
