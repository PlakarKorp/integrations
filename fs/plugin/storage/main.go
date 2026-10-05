package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	fs "github.com/PlakarKorp/integrations/fs/storage"
)

func main() {
	sdk.EntrypointStorage(os.Args, fs.NewStore)
}
