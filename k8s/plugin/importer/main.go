package main

import (
	"context"
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	k8s "github.com/PlakarKorp/integrations/k8s"
)

func main() {
	server.EntrypointImporter(context.Background(), os.Args, k8s.NewImporter)
}
