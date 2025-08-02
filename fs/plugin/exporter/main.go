package main

import (
	sdk "github.com/PlakarKorp/go-kloset-sdk"
	fs "github.com/PlakarKorp/integration-fs/exporter"
)

func main() {
	if err := sdk.RunExporter(fs.NewFSExporter); err != nil {
		panic(err)
	}
}
