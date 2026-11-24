package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	gcs "github.com/PlakarKorp/integrations/gcs"
)

func main() {
	server.EntrypointExporter(gcs.NewExporter, os.Args)
}
