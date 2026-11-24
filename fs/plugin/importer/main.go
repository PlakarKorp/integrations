package main

import (
	"context"
	"errors"
	"os"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/server"
	fs "github.com/PlakarKorp/integrations/fs/importer"
)

type failingImporter struct {
	integration.Importer
}

func (failingImporter) Import(context.Context, chan<- *integration.Record, <-chan *integration.Result) (map[string]string, error) {
	return nil, errors.New("fs integration cannot be used in this context")
}

func failer(ctx context.Context, opts *integration.Options, name string, config map[string]string) (integration.Importer, error) {
	i, err := fs.NewFSImporter(ctx, opts, name, config)
	if err != nil {
		return nil, err
	}
	return failingImporter{i}, nil
}

func main() {
	server.EntrypointImporter(failer, os.Args)
}
