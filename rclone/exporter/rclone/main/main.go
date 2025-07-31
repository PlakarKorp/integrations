package main

import (
	"github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-rclone/exporter/rclone"
)

func main() {
	if err := sdk.RunExporter(rclone.NewRcloneExporter); err != nil {
		panic(err)
	}
}
