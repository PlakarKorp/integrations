package gcs

import (
	"context"
	"fmt"
	"io"
	"path"

	"cloud.google.com/go/storage"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
)

func init() {
	exporter.Register("gs", 0, NewExporter)
}

type gcsExporter struct {
	bucketName string
	path       string
	endp       string

	client *storage.Client
	bucket *storage.BucketHandle
}

func NewExporter(ctx context.Context, _ *connectors.Options, proto string, params map[string]string) (exporter.Exporter, error) {
	bucket, path, endp, opts, err := parse(params, proto)
	if err != nil {
		return nil, err
	}

	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create a google storage client: %w", err)
	}

	return &gcsExporter{
		bucketName: bucket,
		path:       path,
		endp:       endp,

		client: client,
		bucket: client.Bucket(bucket),
	}, nil
}

func (g *gcsExporter) Origin() string {
	if g.endp != "" {
		return g.endp + "/" + g.bucketName
	}
	return g.bucketName + ".storage.googleapis.com"
}

func (g *gcsExporter) Type() string          { return "gs" }
func (g *gcsExporter) Root() string          { return g.path }
func (g *gcsExporter) Flags() location.Flags { return 0 }

func (g *gcsExporter) Ping(ctx context.Context) error {
	_, err := g.client.Bucket(g.bucketName).Attrs(ctx)
	return err
}

func (g *gcsExporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	for record := range records {
		if record.Err != nil || record.IsXattr || !record.FileInfo.Lmode.IsRegular() {
			results <- record.Ok()
			continue
		}

		w := g.bucket.Object(path.Join(g.path, record.Pathname)).NewWriter(ctx)
		_, err := io.Copy(w, record.Reader)
		results <- record.Error(err)
		w.Close()
	}

	return nil
}

func (g *gcsExporter) Close(ctx context.Context) error {
	if g.client != nil {
		return g.client.Close()
	}
	return nil
}
