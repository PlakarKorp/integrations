package main

import (
	"os"

	"github.com/PlakarKorp/go-inventory-sdk/sdk/server"
	"github.com/PlakarKorp/integrations/openstack/inventory"
)

func main() {
	server.Entrypoint(os.Args, inventory.NewInventory)
}
