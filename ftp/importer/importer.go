package importer

import (
	"context"
	"io"
	"log"
	"net/url"
	"os"
	"path"
	"sync"

	"github.com/PlakarKorp/integration-ftp/common"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/secsy/goftp"
)

func init() {
	importer.Register("ftp", 0, NewImporter)
}

type Importer struct {
	host     string
	rootDir  string
	username string
	password string

	client *goftp.Client
}

func NewImporter(appCtx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	target := config["location"]
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, err
	}

	var username string
	if tmp, ok := config["username"]; ok {
		username = tmp
	}
	var password string
	if tmp, ok := config["password"]; ok {
		password = tmp
	}

	if parsed.User != nil {
		if parsed.User.Username() != "" {
			username = parsed.User.Username()
		}
		if p, ok := parsed.User.Password(); ok {
			password = p
		}
	}

	return &Importer{
		host:     parsed.Host,
		rootDir:  parsed.Path,
		username: username,
		password: password,
	}, nil
}

func (p *Importer) walkAndCollectFiles(ctx context.Context, client *goftp.Client, root string, filePaths chan<- string, wg *sync.WaitGroup) {
	defer wg.Done()

	if err := ctx.Err(); err != nil {
		return
	}

	entries, err := client.ReadDir(root)
	if err != nil {
		log.Printf("[FTPImporter] Error reading directory %s: %v", root, err)
		return
	}

	for _, entry := range entries {
		entryPath := path.Join(root, entry.Name())

		if entry.IsDir() {
			wg.Add(1)
			go p.walkAndCollectFiles(ctx, client, entryPath, filePaths, wg)
		} else {
			filePaths <- entryPath
		}
	}
}

func (p *Importer) processFiles(client *goftp.Client, filePaths <-chan string, results chan<- *connectors.Record, wg *sync.WaitGroup) {
	defer wg.Done()

	for filePath := range filePaths {
		info, err := client.Stat(filePath)
		if err != nil {
			results <- connectors.NewError(filePath, err)
			continue
		}

		fileinfo := objects.FileInfoFromStat(info)

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

		results <- connectors.NewRecord(filePath, "", fileinfo, nil, readerFunc)
	}
}

type readerCloser struct {
	File *os.File
}

func (rc readerCloser) Read(p []byte) (int, error) {
	return rc.File.Read(p)
}

func (rc readerCloser) Close() error {
	name := rc.File.Name()
	err := rc.File.Close()
	_ = os.Remove(name)
	return err
}

func (p *Importer) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)
	client, err := common.ConnectToFTP(p.host, p.username, p.password)
	if err != nil {
		return err
	}
	p.client = client

	filePaths := make(chan string, 1000)

	var (
		workerWG sync.WaitGroup
		walkerWG sync.WaitGroup
	)

	// Walk directory tree
	walkerWG.Add(1)
	go p.walkAndCollectFiles(ctx, client, p.rootDir, filePaths, &walkerWG)

	// Close filePaths only after all walk goroutines are done
	go func() {
		walkerWG.Wait()
		close(filePaths)
	}()

	// Launch worker goroutines to process file paths
	numWorkers := 64
	for range numWorkers {
		workerWG.Add(1)
		go p.processFiles(client, filePaths, records, &workerWG)
	}

	// Close results channel after all workers complete
	workerWG.Wait()

	return nil
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
