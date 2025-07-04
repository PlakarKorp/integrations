package main

import (
	"github.com/PlakarKorp/go-kloset-sdk/sdk"
	"github.com/PlakarKorp/notion-integration/notion"
)

func main() {
	err := sdk.RunImporter(notion.NewNotionImporter)
	if err != nil {
		panic(err)
	}
}
