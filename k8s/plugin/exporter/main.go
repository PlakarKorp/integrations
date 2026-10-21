package main

import (
	"context"
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	k8s "github.com/PlakarKorp/integrations/k8s"
)

func main() {
	server.EntrypointExporter(context.Background(), os.Args, k8s.NewExporter)
}
