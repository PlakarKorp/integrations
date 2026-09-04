package main

import (
	"context"
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-redis/importer"
	"github.com/PlakarKorp/integration-redis/redisconn"
	"github.com/PlakarKorp/kloset/connectors"
	iimporter "github.com/PlakarKorp/kloset/connectors/importer"
)

func newRedis(_ context.Context, _ *connectors.Options, proto string, config map[string]string) (iimporter.Importer, error) {
	conn, err := redisconn.ParseConnConfig(config)
	if err != nil {
		return nil, err
	}
	return importer.New(proto, conn, config)
}

func main() { sdk.EntrypointImporter(os.Args, newRedis) }
