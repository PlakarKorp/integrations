package main

import (
	"context"
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integrations/docker"
	tarimporter "github.com/PlakarKorp/integrations/docker/importer"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
)

func newImporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	if name == "docker" {
		return docker.NewImporter(ctx, opts, name, config)
	}
	return tarimporter.NewImporter(ctx, opts, name, config)
}

func main() {
	sdk.EntrypointImporter(os.Args, newImporter)
}
