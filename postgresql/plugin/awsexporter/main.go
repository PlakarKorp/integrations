package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/postgresql/awsexporter"
)

func main() {
	server.EntrypointExporter(awsexporter.NewAWSExporter, os.Args)
}
