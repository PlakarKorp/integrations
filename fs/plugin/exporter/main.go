package main

import (
	"context"
	"errors"
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	fs "github.com/PlakarKorp/integrations/fs/exporter"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
)

type failingExporter struct {
	exporter.Exporter
}

func (failingExporter) Export(context.Context, <-chan *connectors.Record, chan<- *connectors.Result) error {
	return errors.New("fs integration cannot be used in this context")
}

func failer(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	e, err := fs.NewFSExporter(ctx, opts, name, config)
	if err != nil {
		return nil, err
	}
	return failingExporter{e}, nil
}

func main() {
	sdk.EntrypointExporter(os.Args, failer)
}
