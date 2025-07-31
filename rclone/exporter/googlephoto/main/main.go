package main

import (
	"github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-rclone/exporter/googlephoto"
)

func main() {
	if err := sdk.RunExporter(googlephoto.NewGooglePhotoExporter); err != nil {
		panic(err)
	}
}
