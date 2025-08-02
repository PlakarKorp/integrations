package main

import (
	sdk "github.com/PlakarKorp/go-kloset-sdk"
	fs "github.com/PlakarKorp/integration-fs/storage"
)

func main() {
	if err := sdk.RunStorage(fs.NewStore); err != nil {
		panic(err)
	}
}
