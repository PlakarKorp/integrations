package main

import (
	"context"
	"errors"
	"os"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/server"
	fs "github.com/PlakarKorp/integrations/fs/exporter"
)

type failingExporter struct {
	integration.Exporter
}

func (failingExporter) Export(context.Context, map[string]string, <-chan *integration.Record, chan<- *integration.Result) error {
	return errors.New("fs integration cannot be used in this context")
}

func failer(ctx context.Context, opts *integration.Options, name string, config map[string]string) (integration.Exporter, error) {
	e, err := fs.NewFSExporter(ctx, opts, name, config)
	if err != nil {
		return nil, err
	}
	return failingExporter{e}, nil
}

func main() {
	server.EntrypointExporter(failer, os.Args)
}
