package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-backblazeb2/storage"
)

// main is intentionally tiny: Plakar launches this binary as a subprocess,
// and the SDK bridges protocol requests to `storage.NewNativeStore`.
func main() {
	sdk.EntrypointStorage(os.Args, storage.NewNativeStore)
}
