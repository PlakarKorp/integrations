package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	redis "github.com/PlakarKorp/integration-redis"
)

func main() {
	sdk.EntrypointImporter(os.Args, redis.NewImporter)
}
