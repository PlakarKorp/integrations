package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"path"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	fsexporter "github.com/PlakarKorp/integrations/fs/exporter"
	fsimporter "github.com/PlakarKorp/integrations/fs/importer"
	"github.com/PlakarKorp/integrations/k8s/mtls"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/connectors/importer"
)

func chooseImporter(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (importer.Importer, error) {
	switch proto {
	case "fs":
		return fsimporter.NewFSImporter(ctx, opts, proto, config)
	default:
		return nil, fmt.Errorf("unsupported proto %q", proto)
	}
}

func chooseExporter(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (exporter.Exporter, error) {
	switch proto {
	case "fs":
		return fsexporter.NewFSExporter(ctx, opts, proto, config)
	default:
		return nil, fmt.Errorf("unsupported proto %q", proto)
	}
}

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
		peer     string
		port     = 8080
	)

	flag.Usage = usage
	flag.BoolVar(&doexport, "export", false, `run the exporter instead of the fs importer`)
	flag.StringVar(&peer, "peer", "", `hex sha256 of the client public key (required)`)
	flag.IntVar(&port, "p", port, `the port to listen in`)
	flag.Parse()

	if flag.NArg() != 0 {
		usage()
	}

	if peer == "" {
		fatal("missing -peer")
	}
	clientfp, err := mtls.ParseFingerprint(peer)
	if err != nil {
		fatal("bad -peer: %v", err)
	}

	cert, myfp, err := mtls.Gencert()
	if err != nil {
		fatal("failed to generate a key: %v", err)
	}

	fmt.Fprintf(os.Stderr, "plakar-pubkey: %s\n", mtls.Fingerprint(myfp))

	listener, err := tls.Listen("tcp", fmt.Sprintf(":%d", port),
		mtls.ServerTlsConfig(cert, clientfp))
	if err != nil {
		fatal("failed to listen on port %d: %s", port, err)
	}

	fmt.Fprintf(os.Stderr, "listening on :%d\n", port)

	if doexport {
		if err := sdk.RunExporterOn(chooseExporter, listener); err != nil {
			fatal("failed to run the exporter: %s", err)
		}
	} else {
		if err := sdk.RunImporterOn(chooseImporter, listener); err != nil {
			fatal("failed to run the importer: %s", err)
		}
	}
}
