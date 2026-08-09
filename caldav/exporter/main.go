package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
)

func main() {
	sdk.EntrypointExporter(os.Args, NewCaldavExporter)
}
