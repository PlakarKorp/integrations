package main

import (
	"github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/notion-integration/notion"
)

func main() {
	err := sdk.RunExporter(notion.NewNotionExporter)
	if err != nil {
		panic(err)
	}
}
