package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integrations/rclone"
)

func main() {
	sdk.EntrypointStorage(os.Args, rclone.NewStorage)
}
