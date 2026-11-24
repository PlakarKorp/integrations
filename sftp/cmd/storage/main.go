package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
	"github.com/PlakarKorp/integrations/sftp"
)

func main() {
	server.EntrypointStorage(sftp.NewStore, os.Args)
}
