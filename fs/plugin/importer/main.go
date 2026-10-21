package main

import (
	"context"
	"errors"
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	fs "github.com/PlakarKorp/integrations/fs/importer"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
)

type failingImporter struct {
	importer.Importer
}

func (failingImporter) Import(context.Context, chan<- *connectors.Record, <-chan *connectors.Result) error {
	return errors.New("fs integration cannot be used in this context")
}

func failer(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	i, err := fs.NewFSImporter(ctx, opts, name, config)
	if err != nil {
		return nil, err
	}
	return failingImporter{i}, nil
}

func main() {
	sdk.EntrypointImporter(os.Args, failer)
}
