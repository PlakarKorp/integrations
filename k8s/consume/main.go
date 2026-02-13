package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path"

	gimporter "github.com/PlakarKorp/integration-grpc/importer"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s: ", path.Base(os.Args[0]))
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

func progress(ctx context.Context, imp importer.Importer, fn func(<-chan *connectors.Record, chan<- *connectors.Result)) error {
	var (
		size    = 2
		records = make(chan *connectors.Record, size)
		retch   = make(chan struct{}, 1)
	)

	var results chan *connectors.Result
	if (imp.Flags() & location.FLAG_NEEDACK) != 0 {
		results = make(chan *connectors.Result, size)
	}

	go func() {
		fn(records, results)
		if results != nil {
			close(results)
		}
		close(retch)
	}()

	err := imp.Import(ctx, records, results)
	<-retch
	return err
}

func main() {
	flag.Parse()
	dest := flag.Arg(0)

	client, err := grpc.NewClient(dest, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fatal("failed to create a grpc client for %s: %s", dest, err)
	}

	path := "/home/op/w/pk/plakar/docs"

	opts := &connectors.Options{
		Hostname:        "foobar",
		OperatingSystem: "linux",
		Architecture:    "amd64",
		CWD:             path,
		MaxConcurrency:  2,
	}

	importer, err := gimporter.NewImporter(context.Background(), client, opts, "fs", map[string]string{
		"location":         "fs://" + path,
		"dont_traverse_fs": "true",
	})
	if err != nil {
		fatal("failed to instantiate the importer: %s", err)
	}

	err = progress(context.Background(), importer, func(records <-chan *connectors.Record, results chan<- *connectors.Result) {
		for record := range records {
			if results == nil {
				record.Close()
			} else {
				results <- record.Ok()
			}

			fmt.Println("->", record.Pathname)
		}
	})
	if err != nil {
		fatal("failed to run progress: %s", err)
	}
}
