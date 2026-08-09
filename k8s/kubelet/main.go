package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"path"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	fsexporter "github.com/PlakarKorp/integrations/fs/exporter"
	fsimporter "github.com/PlakarKorp/integrations/fs/importer"
)

func usage() {
	fmt.Fprintf(os.Stderr, "usage: %s [-export] [-p port]\n", path.Base(os.Args[0]))
	os.Exit(1)
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s: ", path.Base(os.Args[0]))
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

func main() {
	var (
		doexport bool
		port     = 8080
	)

	flag.Usage = usage
	flag.BoolVar(&doexport, "export", false, `run the exporter instead of the fs importer`)
	flag.IntVar(&port, "p", port, `the port to listen in`)
	flag.Parse()

	if flag.NArg() != 0 {
		usage()
	}

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		fatal("failed to listen on port %s: %s", port, err)
	}

	fmt.Fprintf(os.Stderr, "listening on :%d\n", port)

	if doexport {
		if err := sdk.RunExporterOn(fsexporter.NewFSExporter, listener); err != nil {
			fatal("failed to run the fs exporter: %s", err)
		}
	} else {
		if err := sdk.RunImporterOn(fsimporter.NewFSImporter, listener); err != nil {
			fatal("failed to run the fs importer: %s", err)
		}
	}
}
