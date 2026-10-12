package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
<<<<<<<< HEAD:imap/plugin/exporter/main.go
	"github.com/PlakarKorp/integrations/imap/exporter"
========
	"github.com/PlakarKorp/integrations/smb/exporter"
>>>>>>>> integration/smb:smb/plugin/exporter/main.go
)

func main() {
	sdk.EntrypointExporter(os.Args, exporter.NewExporter)
}
