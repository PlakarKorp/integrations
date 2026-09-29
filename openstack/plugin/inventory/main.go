package main

import (
	"os"

	"github.com/PlakarKorp/go-inventory-sdk/sdk/server"
	osinv "github.com/PlakarKorp/integrations-private/openstack"
)

func main() {
	server.Entrypoint(os.Args, osinv.NewInventory)
}
