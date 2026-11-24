package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	k8s "github.com/PlakarKorp/integrations/k8s"
)

func main() {
	server.EntrypointExporter(k8s.NewExporter, os.Args)
}
