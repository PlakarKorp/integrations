package gcs

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"

	"cloud.google.com/go/storage"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/snapshot/importer"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

type gcsImporter struct {
	bucketName string
	path       string
	base       string
	opts       []option.ClientOption

	client *storage.Client
	bucket *storage.BucketHandle
}

func NewImporter(ctx context.Context, _ *importer.Options, proto string, params map[string]string) (importer.Importer, error) {
	bucket, path, opts, err := parse(params, proto)
	if err != nil {
		return nil, err
	}

	return &gcsImporter{
		bucketName: bucket,
		path:       path,
		base:       "/" + path,
		opts:       opts,
	}, nil
}

func (g *gcsImporter) Scan(ctx context.Context) (<-chan *importer.ScanResult, error) {
	client, err := storage.NewClient(ctx, g.opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create a GCS client: %w", err)
	}

	g.client = client
	g.bucket = client.Bucket(g.bucketName)

	results := make(chan *importer.ScanResult, 1000)
	go g.scan(ctx, results)
	return results, nil
}

func (g *gcsImporter) mkparents(results chan<- *importer.ScanResult, p string) {
	for {
		fi := objects.FileInfo{
			Lname: path.Base(p),
			Lmode: 0700 | os.ModeDir,
		}
		results <- importer.NewScanRecord(p, "", fi, nil, nil)

		if p == "/" {
			break
		}
		p = path.Dir(p)
	}
}

func (g *gcsImporter) scan(ctx context.Context, results chan<- *importer.ScanResult) {
	defer close(results)

	g.mkparents(results, g.base)

	query := storage.Query{Prefix: g.path}
	it := g.bucket.Objects(ctx, &query)
	for {
		obj, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			results <- importer.NewScanError(g.path, err)
			break
		}

		fullpath := "/" + obj.Name

		g.mkparents(results, path.Dir(fullpath))

		fi := objects.FileInfo{
			Lname:     path.Base(obj.Name),
			Lsize:     obj.Size,
			Lmode:     0644,
			LmodTime:  obj.Updated,
			Lusername: obj.Owner,
		}

		results <- importer.NewScanRecord(fullpath, "", fi, nil, func() (io.ReadCloser, error) {
			return g.bucket.Object(obj.Name).NewReader(ctx)
		})
	}
}

func (g *gcsImporter) Location(ctx context.Context) (string, error) {
	return "gcs://" + path.Join(g.bucketName, g.path), nil
}
func (g *gcsImporter) Origin(ctx context.Context) (string, error) { return g.bucketName, nil }
func (g *gcsImporter) Type(ctx context.Context) (string, error)   { return "gcs", nil }
func (g *gcsImporter) Root(ctx context.Context) (string, error)   { return g.base, nil }

func (g *gcsImporter) Close(ctx context.Context) error {
	if g.client != nil {
		return g.client.Close()
	}
	return nil
}
