package gcs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/snapshot/exporter"
)

func init() {
	exporter.Register("gcs", 0, NewExporter)
}

type gcsExporter struct {
	bucketName string
	path       string

	client *storage.Client
	bucket *storage.BucketHandle
}

func NewExporter(ctx context.Context, _ *exporter.Options, proto string, params map[string]string) (exporter.Exporter, error) {
	bucket, path, opts, err := parse(params, proto)
	if err != nil {
		return nil, err
	}

	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create a GCS client: %w", err)
	}

	return &gcsExporter{
		bucketName: bucket,
		path:       path,

		client: client,
		bucket: client.Bucket(bucket),
	}, nil
}

func (g *gcsExporter) realpath(rel string) string { return path.Join(g.path, rel) }

func (g *gcsExporter) Root(ctx context.Context) (string, error) { return g.path, nil }

func (g *gcsExporter) CreateDirectory(ctx context.Context, pathname string) error { return nil }

func (g *gcsExporter) StoreFile(ctx context.Context, pathname string, fp io.Reader, size int64) error {
	pathname = g.realpath(strings.TrimLeft(pathname, "/"))

	w := g.bucket.Object(pathname).NewWriter(ctx)
	if _, err := io.Copy(w, fp); err != nil {
		return err
	}
	return w.Close()
}

func (g *gcsExporter) SetPermissions(ctx context.Context, pathname string, fileinfo *objects.FileInfo) error {
	return nil
}

func (g *gcsExporter) CreateLink(ctx context.Context, oldname string, newname string, ltype exporter.LinkType) error {
	return errors.ErrUnsupported
}

func (g *gcsExporter) Close(ctx context.Context) error {
	if g.client != nil {
		return g.client.Close()
	}
	return nil
}
