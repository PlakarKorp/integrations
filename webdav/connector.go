package webdav

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/location"
	"github.com/emersion/go-webdav"
	"golang.org/x/sync/errgroup"
)

type WebDAV struct {
	client      *webdav.Client
	location    *url.URL
	concurrency int
}

// CustomUserAgentTransport override HTTP client to change User-Agent
type CustomUserAgentTransport struct {
	Transport http.RoundTripper
}

func (c *CustomUserAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", "PlakarBackup/1.0")
	return c.Transport.RoundTrip(req)
}

func init() {
	_ = integration.RegisterImporter("dav", 0, NewImporter)
	_ = integration.RegisterImporter("davs", 0, NewImporter)

	_ = integration.RegisterExporter("dav", 0, NewExporter)
	_ = integration.RegisterExporter("davs", 0, NewExporter)
}

func New(ctx context.Context, opts *integration.Options, name string, params map[string]string, baseClient webdav.HTTPClient) (*WebDAV, error) {

	customTransport := &CustomUserAgentTransport{Transport: http.DefaultTransport}

	if baseClient == nil {
		baseClient = &http.Client{
			Transport: customTransport,
		}
	}

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
			return nil, errors.New("to use dav:// insecure needs to be set as well, use davs:// for an encrypted tunnel")
		}
		loc.Scheme = "http"

	case "davs":
		if insecure, _ := strconv.ParseBool(params["insecure"]); insecure {
			return nil, errors.New("cannot use davs:// with insecure=true")
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

func NewImporter(ctx context.Context, opts *integration.Options, name string, params map[string]string) (integration.Importer, error) {
	return New(ctx, opts, name, params, nil)
}

func NewExporter(ctx context.Context, opts *integration.Options, name string, params map[string]string) (integration.Exporter, error) {
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

func (w *WebDAV) Import(ctx context.Context, records chan<- *integration.Record, results <-chan *integration.Result) (map[string]string, error) {
	defer close(records)

	var wg errgroup.Group
	wg.SetLimit(w.concurrency)

	return nil, w.walk(ctx, func(p string, sb *webdav.FileInfo, err error) error {
		if err != nil {
			err = fmt.Errorf("failed to walk %s: %w", p, err)
			if p == w.location.Path {
				return err
			}
			records <- integration.NewError(p, err)
			return nil
		}

		finfo := integration.FileInfo{
			Lname:    path.Base(p),
			Lsize:    sb.Size,
			Lmode:    0644,
			LmodTime: sb.ModTime,
		}

		if sb.IsDir {
			finfo.Lmode = fs.ModeDir
		}

		records <- integration.NewRecord(p, "", finfo, nil, func() (io.ReadCloser, error) {
			return w.client.Open(ctx, p)
		})

		return nil
	})
}

// contained joins a record pathname onto root and refuses anything that would
// land outside of it.
//
// path.Join cleans as it goes, so a record pathname of "/../../etc/x" would
// otherwise resolve above the collection we were pointed at and be written
// there.  WebDAV paths are always slash-separated.
func contained(root, pathname string) (string, error) {
	joined := path.Join(root, pathname)
	cleanRoot := path.Clean(root)

	if joined != cleanRoot && !strings.HasPrefix(joined, strings.TrimSuffix(cleanRoot, "/")+"/") {
		return "", fmt.Errorf("path %q escapes the restore root %q", pathname, cleanRoot)
	}
	return joined, nil
}

func (w *WebDAV) Export(ctx context.Context, _ map[string]string, records <-chan *integration.Record, results chan<- *integration.Result) error {
	defer close(results)

	for record := range records {
		if record.Err != nil || record.IsXattr {
			results <- record.Ok()
			continue
		}

		dest, err := contained(w.location.Path, record.Pathname)
		if err != nil {
			results <- record.Error(err)
			continue
		}

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
			_ = fp.Close()
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
