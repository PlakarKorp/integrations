package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-rclone/storage"
)

func main() {
	sdk.EntrypointStorage(os.Args, storage.NewRcloneStorage)
}
