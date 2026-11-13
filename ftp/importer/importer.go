package importer

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"sync"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/location"
	"github.com/PlakarKorp/integrations/ftp/conn"
)

func init() {
	_ = integration.RegisterImporter("ftp", 0, NewImporter)
}

type Importer struct {
	host     string
	rootDir  string
	connOpts conn.Options
	port     string

	client *conn.Client
}

func NewImporter(appCtx context.Context, opts *integration.Options, name string, config map[string]string) (integration.Importer, error) {
	target := config["location"]
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, err
	}

	connOpts, err := conn.ParseOptions(config)
	if err != nil {
		return nil, err
	}

	var port string
	if tmp, ok := config["port"]; ok {
		port = tmp
	}
	var root string
	if tmp, ok := config["root"]; ok {
		root = tmp
	}

	if parsed.User != nil {
		if parsed.User.Username() != "" {
			connOpts.Username = parsed.User.Username()
		}
		if p, ok := parsed.User.Password(); ok {
			connOpts.Password = p
		}
	}

	rootDir := parsed.Path
	if root != "" {
		rootDir = root
	}
	if rootDir == "" {
		rootDir = "/"
	}

	host := parsed.Host
	if parsed.Port() == "" && port != "" {
		host = fmt.Sprintf("%s:%s", parsed.Host, port)
	}

	return &Importer{
		host:     host,
		rootDir:  rootDir,
		connOpts: connOpts,
		port:     port,
	}, nil
}

func (p *Importer) walkAndCollectFiles(ctx context.Context, client *conn.Client, dir string, filePaths chan<- string, records chan<- *integration.Record, wg *sync.WaitGroup) {
	if err := ctx.Err(); err != nil {
		return
	}

	entries, err := client.ReadDir(dir)
	if err != nil {
		records <- integration.NewError(dir, err)
		return
	}
	p.collectEntries(ctx, client, dir, entries, filePaths, records, wg)
}

func (p *Importer) collectEntries(ctx context.Context, client *conn.Client, dir string, entries []os.FileInfo, filePaths chan<- string, records chan<- *integration.Record, wg *sync.WaitGroup) {
	for _, entry := range entries {
		entryPath := path.Join(dir, entry.Name())

		if entry.IsDir() {
			wg.Go(func() { p.walkAndCollectFiles(ctx, client, entryPath, filePaths, records, wg) })
		} else {
			filePaths <- entryPath
		}
	}
}

func (p *Importer) processFiles(client *conn.Client, filePaths <-chan string, results chan<- *integration.Record) {
	for filePath := range filePaths {
		info, err := client.Stat(filePath)
		if err != nil {
			results <- integration.NewError(filePath, err)
			continue
		}

		fileinfo := integration.FileInfoFromStat(info)

		readerFunc := func() (io.ReadCloser, error) {
			pr, pw := io.Pipe()

			go func() {
				if err := client.Retrieve(filePath, pw); err != nil {
					_ = pw.CloseWithError(err)
					return
				}
				_ = pw.Close()
			}()

			// Return the reader side. Closing it will unblock the writer (Retrieve) if the consumer stops early.
			return pr, nil
		}

		results <- integration.NewRecord(filePath, "", fileinfo, nil, readerFunc)
	}
}

func (p *Importer) Import(ctx context.Context, records chan<- *integration.Record, results <-chan *integration.Result) (map[string]string, error) {
	defer close(records)
	client, err := conn.ConnectToFTP(p.host, p.connOpts)
	if err != nil {
		return nil, err
	}
	p.client = client

	// List root first to return an error, not an empty backup
	entries, err := client.ReadDir(p.rootDir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", p.rootDir, err)
	}

	filePaths := make(chan string, 1000)

	var (
		workerWG sync.WaitGroup
		walkerWG sync.WaitGroup
	)

	// Walk directory tree
	walkerWG.Go(func() { p.collectEntries(ctx, client, p.rootDir, entries, filePaths, records, &walkerWG) })

	// Close filePaths only after all walk goroutines are done
	go func() {
		walkerWG.Wait()
		close(filePaths)
	}()

	// Launch worker goroutines to process file paths
	numWorkers := 64
	for range numWorkers {
		workerWG.Go(func() { p.processFiles(client, filePaths, records) })
	}

	// Close results channel after all workers complete
	workerWG.Wait()

	return nil, nil //nolint:nilnil // no import metadata
}

func (p *Importer) Ping(ctx context.Context) error {
	if p.client != nil {
		_, err := p.client.Stat(p.Root())
		return err
	}
	return nil
}

func (p *Importer) Close(ctx context.Context) error {
	if p.client != nil {
		return p.client.Close()
	}
	return nil
}

func (p *Importer) Root() string {
	return p.rootDir
}

func (p *Importer) Origin() string {
	return p.host
}

func (p *Importer) Type() string {
	return "ftp"
}

func (p *Importer) Flags() location.Flags {
	return 0
}
