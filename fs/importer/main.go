package main

import (
	"github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-fs"
)

func main() {
	if err := sdk.RunImporter(fs.NewFSImporter); err != nil {
		panic(err)
	}
}
