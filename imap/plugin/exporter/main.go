package main

import (
	"fmt"
	"os"

	"github.com/PlakarKorp/go-kloset-sdk"
	"github.com/PlakarKorp/integration-imap/exporter"
)

func main() {
	if len(os.Args) != 1 {
		fmt.Printf("Usage: %s\n", os.Args[0])
		os.Exit(1)
	}

	if err := sdk.RunExporter(exporter.NewImapExporter); err != nil {
		panic(err)
	}
}
