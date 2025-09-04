package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/notion-integration/notion"
)

func main() {
	sdk.EntrypointExporter(os.Args, notion.NewNotionExporter)
}
