package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integrations/imap/store"
)

func main() {
	sdk.EntrypointStorage(os.Args, store.NewStore)
}
