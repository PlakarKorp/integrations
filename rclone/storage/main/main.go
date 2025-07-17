package main

import (
	"github.com/PlakarKorp/go-kloset-sdk/sdk"
	"github.com/PlakarKorp/integration-rclone/storage"
)

func main() {
	if err := sdk.RunStorage(storage.NewRcloneStorage); err != nil {
		panic(err)
	}
}
