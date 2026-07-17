package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integrations/postgresql/awsimporter"
)

func main() {
	sdk.EntrypointImporter(os.Args, awsimporter.NewAWSImporter)
}
