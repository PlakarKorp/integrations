package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integrations/imap/exporter"
)

func main() {
	sdk.EntrypointExporter(os.Args, exporter.NewImapExporter)
}
