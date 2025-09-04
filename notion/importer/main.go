package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/notion-integration/notion"
)

func main() {
	sdk.EntrypointImporter(os.Args, notion.NewNotionImporter)
}
