package main

import (
	"context"
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-redis/exporter"
	"github.com/PlakarKorp/kloset/connectors"
	exporterIface "github.com/PlakarKorp/kloset/connectors/exporter"
)

func newRedis(_ context.Context, _ *connectors.Options, proto string, config map[string]string) (exporterIface.Exporter, error) {
	return exporter.New(proto, config)
}

func main() { sdk.EntrypointExporter(os.Args, newRedis) }
