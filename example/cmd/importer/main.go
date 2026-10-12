package main

import (
	"context"
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	connector "github.com/PlakarKorp/integrations/example"
)

func main() {
	ctx := sdk.InterruptableContext(context.Background())
	sdk.EntrypointImporter(ctx, os.Args, connector.NewImporter)
}
