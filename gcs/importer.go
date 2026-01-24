package gcs

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"google.golang.org/api/iterator"
)

func init() {
	importer.Register("gs", 0, NewImporter)
}

type gcsImporter struct {
	bucketName string
	path       string
	base       string

	client *storage.Client
	bucket *storage.BucketHandle
}

func NewImporter(ctx context.Context, _ *connectors.Options, proto string, params map[string]string) (importer.Importer, error) {
	bucket, path, opts, err := parse(params, proto)
	if err != nil {
		return nil, err
	}

	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create a google storage client: %w", err)
	}

	return &gcsImporter{
		bucketName: bucket,
		path:       path,
		base:       "/" + path,
		client:     client,
		bucket:     client.Bucket(bucket),
	}, nil
}

func (g *gcsImporter) Origin() string        { return g.bucketName }
func (g *gcsImporter) Type() string          { return "gs" }
func (g *gcsImporter) Root() string          { return g.base }
func (g *gcsImporter) Flags() location.Flags { return 0 }

func (g *gcsImporter) Ping(ctx context.Context) error {
	_, err := g.client.Bucket(g.bucketName).Attrs(ctx)
	return err
}

func (g *gcsImporter) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	query := storage.Query{Prefix: g.path}
	it := g.bucket.Objects(ctx, &query)
	for {
		obj, err := it.Next()
		if err == iterator.Done {
			return nil
		}
		if err != nil {
			return err
		}

		// DO NOT REMOVE THIS.
		// This is needed to skip "directory" objects in GCS,
		// which are just objects with names ending in "/" when created from their UI.
		if strings.HasSuffix(obj.Name, "/") {
			// skip "directory" objects
			continue
		}

		fullpath := "/" + obj.Name

		fi := objects.FileInfo{
			Lname:     path.Base(obj.Name),
			Lsize:     obj.Size,
			Lmode:     0644,
			LmodTime:  obj.Updated,
			Lusername: obj.Owner,
		}

		records <- connectors.NewRecord(fullpath, "", fi, nil, func() (io.ReadCloser, error) {
			return g.bucket.Object(obj.Name).NewReader(ctx)
		})
	}
}

func (g *gcsImporter) Close(ctx context.Context) error {
	if g.client != nil {
		return g.client.Close()
	}
	return nil
}
