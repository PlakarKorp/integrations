package main

import (
	"github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-rclone/importer/rclone"
)

func main() {
	if err := sdk.RunImporter(rclone.NewRcloneImporter); err != nil {
		panic(err)
	}
}
