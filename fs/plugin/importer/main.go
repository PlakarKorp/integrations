package main

import (
	sdk "github.com/PlakarKorp/go-kloset-sdk"
	fs "github.com/PlakarKorp/integration-fs/importer"
)

func main() {
	if err := sdk.RunImporter(fs.NewFSImporter); err != nil {
		panic(err)
	}
}
