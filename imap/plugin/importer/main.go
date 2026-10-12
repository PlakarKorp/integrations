package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
<<<<<<<< HEAD:imap/plugin/importer/main.go
	"github.com/PlakarKorp/integrations/imap/importer"
========
	"github.com/PlakarKorp/integrations/smb/importer"
>>>>>>>> integration/smb:smb/plugin/importer/main.go
)

func main() {
	sdk.EntrypointImporter(os.Args, importer.NewImporter)
}
