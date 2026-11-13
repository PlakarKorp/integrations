package main

import (
	"os"

	"github.com/PlakarKorp/integration-sdk/server"
)

func main() {
	server.EntrypointExporter(NewCaldavExporter, os.Args)
}
