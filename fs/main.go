package main

import (
	"github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-fs/fs"
)

func main() {
	if err := sdk.RunStorage(fs.NewStore); err != nil {
		panic(err)
	}
}
