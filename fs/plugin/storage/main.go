package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	fs "github.com/PlakarKorp/integration-fs/storage"
)

func main() {
	sdk.EntrypointStorage(os.Args, fs.NewStore)
}
