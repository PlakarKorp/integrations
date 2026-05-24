package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-postgresql/awsexporter"
)

func main() {
	sdk.EntrypointExporter(os.Args, awsexporter.NewAWSExporter)
}
