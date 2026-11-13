package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/postgresql/awsimporter"
)

func main() {
	server.EntrypointImporter(awsimporter.NewAWSImporter, os.Args)
}
