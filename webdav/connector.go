package webdav

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path"
	"strconv"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/emersion/go-webdav"
	"golang.org/x/sync/errgroup"
)

type WebDAV struct {
	client      *webdav.Client
	location    *url.URL
	concurrency int
}

func init() {
	importer.Register("dav", 0, NewImporter)
	importer.Register("davs", 0, NewImporter)

	exporter.Register("dav", 0, NewExporter)
	exporter.Register("davs", 0, NewExporter)
}

func New(ctx context.Context, opts *connectors.Options, name string, params map[string]string, baseClient webdav.HTTPClient) (*WebDAV, error) {
	httpc := baseClient
	if u, ok := params["username"]; ok {
		httpc = webdav.HTTPClientWithBasicAuth(httpc, u, params["password"])
	}

	loc, err := url.Parse(params["location"])
	if err != nil {
		return nil, fmt.Errorf("failed to parse location: %w", err)
	}

	if loc.Path == "" {
		loc.Path = "/"
	}

	switch name {
	case "dav":
		if insecure, _ := strconv.ParseBool(params["insecure"]); !insecure {
			return nil, fmt.Errorf("to use dav:// insecure needs to be set as well, use davs:// for an encrypted tunnel")
		}
		loc.Scheme = "http"

	case "davs":
		if insecure, _ := strconv.ParseBool(params["insecure"]); insecure {
			return nil, fmt.Errorf("cannot use davs:// with insecure=true")
		}
		loc.Scheme = "https"

	default:
		return nil, fmt.Errorf("unsupported type %q", name)
	}

	client, err := webdav.NewClient(httpc, loc.String())
	if err != nil {
		return nil, fmt.Errorf("failed to create a webdav client: %w", err)
	}

	return &WebDAV{
		client:      client,
		location:    loc,
		concurrency: opts.MaxConcurrency,
	}, nil
}

func NewImporter(ctx context.Context, opts *connectors.Options, name string, params map[string]string) (importer.Importer, error) {
	return New(ctx, opts, name, params, nil)
}

func NewExporter(ctx context.Context, opts *connectors.Options, name string, params map[string]string) (exporter.Exporter, error) {
	return New(ctx, opts, name, params, nil)
}

func (w *WebDAV) Type() string          { return "webdav" }
func (w *WebDAV) Origin() string        { return w.location.Host }
func (w *WebDAV) Root() string          { return w.location.Path }
func (w *WebDAV) Flags() location.Flags { return 0 }

func (w *WebDAV) Ping(ctx context.Context) error {
	_, err := w.client.Stat(ctx, w.location.Path)
	return err
}

func (w *WebDAV) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	var wg errgroup.Group
	wg.SetLimit(w.concurrency)

	return w.walk(ctx, func(p string, sb *webdav.FileInfo, err error) error {
		if err != nil {
			err = fmt.Errorf("failed to walk %s: %w", p, err)
			if p == w.location.Path {
				return err
			}
			records <- connectors.NewError(p, err)
			return nil
		}

		finfo := objects.FileInfo{
			Lname:    path.Base(p),
			Lsize:    sb.Size,
			Lmode:    0644,
			LmodTime: sb.ModTime,
		}

		if sb.IsDir {
			finfo.Lmode = fs.ModeDir
		}

		records <- connectors.NewRecord(p, "", finfo, nil, func() (io.ReadCloser, error) {
			return w.client.Open(ctx, p)
		})

		return nil
	})
}

func (w *WebDAV) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	for record := range records {
		if record.Err != nil || record.IsXattr {
			results <- record.Ok()
			continue
		}

		dest := path.Join(w.location.Path, record.Pathname)

		if record.FileInfo.IsDir() {
			err := w.client.Mkdir(ctx, dest)
			if err != nil {
				err = fmt.Errorf("cannot mkdir %s: %w",
					dest, err)
			}
			results <- record.Error(err)
			continue
		}

		if !record.FileInfo.Mode().IsRegular() {
			results <- record.Ok()
			continue
		}

		fp, err := w.client.Create(ctx, dest)
		if err != nil {
			err = fmt.Errorf("cannot create %s: %w", dest, err)
			results <- record.Error(err)
			continue
		}

		if _, err := io.Copy(fp, record.Reader); err != nil {
			fp.Close()
			err = fmt.Errorf("write failed: %w", err)
			results <- record.Error(err)
			continue
		}

		if err := fp.Close(); err != nil {
			err = fmt.Errorf("close failed: %w", err)
			results <- record.Error(err)
			continue
		}

		results <- record.Ok()
	}

	return nil
}

func (w *WebDAV) Close(ctx context.Context) error {
	return nil
}
