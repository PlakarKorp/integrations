package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/client"
	"github.com/PlakarKorp/integration-sdk/location"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s: ", path.Base(os.Args[0]))
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

func progress(ctx context.Context, imp integration.Importer, fn func(<-chan *integration.Record, chan<- *integration.Result)) error {
	var (
		size    = 2
		records = make(chan *integration.Record, size)
		retch   = make(chan struct{}, 1)
	)

	var results chan *integration.Result
	if (imp.Flags() & location.FLAG_NEEDACK) != 0 {
		results = make(chan *integration.Result, size)
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

	gclient, err := grpc.NewClient(dest, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fatal("failed to create a grpc client for %s: %s", dest, err)
	}

	path := "/home/op/w/pk/plakar/docs"

	opts := &integration.Options{
		Hostname:        "foobar",
		OperatingSystem: "linux",
		Architecture:    "amd64",
		CWD:             path,
		MaxConcurrency:  2,
	}

	importer, err := client.NewImporter(context.Background(), gclient, opts, "fs", map[string]string{
		"location":         "fs://" + path,
		"dont_traverse_fs": "true",
	})
	if err != nil {
		fatal("failed to instantiate the importer: %s", err)
	}

	err = progress(context.Background(), importer, func(records <-chan *integration.Record, results chan<- *integration.Result) {
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
